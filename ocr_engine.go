//go:build linux && arm64

package pureocr

// ocr_engine.go — Go reimplementation of CWeChatOCR (swigger/wechat-ocr).
//
// Protocol (wx4 / Linux):
//   1. Init:  dlopen libmmmojo.so, InitializeMMMojo(0,0)
//   2. Start: CreateMMMojoEnvironment
//             SetMMMojoEnvironmentCallbacks(kMMReadPush=1, onReadPush)
//             SetMMMojoEnvironmentCallbacks(kMMReadPull=2, nop)
//             SetMMMojoEnvironmentCallbacks(kMMReadShared=3, nop)
//             SetMMMojoEnvironmentCallbacks(kMMRemoteConnect=4, onConnect)
//             SetMMMojoEnvironmentCallbacks(kMMRemoteDisconnect=5, onDisconnect)
//             SetMMMojoEnvironmentCallbacks(kMMRemoteProcessLaunched=6, nop)
//             SetMMMojoEnvironmentCallbacks(kMMRemoteProcessLaunchFailed=7, onFailed)
//             SetMMMojoEnvironmentCallbacks(kMMRemoteMojoError=8, onError)
//             SetMMMojoEnvironmentInitParams(kMMHostProcess=0, true)
//             SetMMMojoEnvironmentInitParams(kMMExePath=2, exePath)
//             AppendMMSubProcessSwitchNative("no-sandbox", "")
//             StartMMMojoEnvironment
//   3. Handshake: wxocr sends HAND_SHAKE(10001) → OCRSupportMessage{supported}
//                 we wait for this before sending OCR requests
//   4. OCR: CreateMMMojoWriteInfo(kMMPush=1, false, REQ_OCR=10010)
//           GetMMMojoWriteInfoRequest(writeInfo, dataSize) → buf
//           copy(buf, protobuf)
//           SendMMMojoWriteInfo(env, writeInfo)
//   5. Response: ReadPush with request_id=RESP_OCR(10011) → ParseOCRRespMessage

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

type engineState int32

const (
	statePending   engineState = 0
	stateInited    engineState = 1  // handshake done, ready for OCR
	stateFailed    engineState = -1
)

// ocrEngine is the Go equivalent of CWeChatOCR.
type ocrEngine struct {
	mojo   *mojoLib
	env    uintptr
	exeDir string // directory containing wxocr and ocr_model/

	mu    sync.Mutex
	cond  *sync.Cond
	state engineState

	// pending tasks: task_id → result channel
	taskSeq uint64 // atomic
	tasks   sync.Map // uint64 → chan string

	// purego callback handles (must stay alive)
	cbReadPush   uintptr
	cbConnect    uintptr
	cbDisconnect uintptr
	cbLaunched   uintptr
	cbFailed     uintptr
	cbError      uintptr
	cbNop1       uintptr
	cbNop2       uintptr

	// C strings passed to libmmmojo — must outlive start()
	cstrExePath      []byte
	cstrNoSandboxKey []byte
	cstrNoSandboxVal []byte
}

func newOCREngine(mojo *mojoLib, wxocrPath, exeDir string) (*ocrEngine, error) {
	e := &ocrEngine{
		mojo:   mojo,
		exeDir: exeDir,
		state:  statePending,
	}
	e.cond = sync.NewCond(&e.mu)

	if err := e.start(wxocrPath); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *ocrEngine) start(wxocrPath string) error {
	env := e.mojo.CreateMMMojoEnvironment()
	if env == 0 {
		return fmt.Errorf("CreateMMMojoEnvironment returned nil")
	}
	e.env = env

	// Do NOT set slot 0 (kMMUserData). Instead, capture `e` directly in each
	// closure — that is how libocr.so's CMojoCall_Mid::Start works too (C++ lambda
	// captures `this`). Setting slot 0 to a non-function-pointer value appears to
	// corrupt the Mojo broker and causes a segfault.

	// slot 1 = kMMReadPush
	e.cbReadPush = purego.NewCallback(func(requestID uint32, readInfo uintptr, userdata uintptr) {
		var pbSize uint32
		pbPtr := e.mojo.GetMMMojoReadInfoRequest(readInfo, uintptr(unsafe.Pointer(&pbSize)))
		if pbPtr != 0 && pbSize > 0 {
			pb := make([]byte, int(pbSize))
			copy(pb, ptrToSlice(pbPtr, int(pbSize)))
			e.onReadPush(requestID, pb)
		}
		e.mojo.RemoveMMMojoReadInfo(readInfo)
	})
	e.mojo.SetMMMojoEnvironmentCallbacks(env, kMMReadPush, e.cbReadPush)

	// slot 2 = kMMReadPull (nop)
	e.cbNop1 = purego.NewCallback(func(requestID uint32, readInfo uintptr, userdata uintptr) {
		e.mojo.RemoveMMMojoReadInfo(readInfo)
	})
	e.mojo.SetMMMojoEnvironmentCallbacks(env, kMMReadPull, e.cbNop1)

	// slot 3 = kMMReadShared (nop)
	e.cbNop2 = purego.NewCallback(func(requestID uint32, readInfo uintptr, userdata uintptr) {
		e.mojo.RemoveMMMojoReadInfo(readInfo)
	})
	e.mojo.SetMMMojoEnvironmentCallbacks(env, kMMReadShared, e.cbNop2)

	// slot 4 = kMMRemoteConnect
	e.cbConnect = purego.NewCallback(func(isConnected bool, userdata uintptr) {
		if !isConnected {
			e.mu.Lock()
			if e.state == statePending {
				e.state = stateFailed
				e.cond.Broadcast()
			}
			e.mu.Unlock()
		}
	})
	e.mojo.SetMMMojoEnvironmentCallbacks(env, kMMRemoteConnect, e.cbConnect)

	// slot 5 = kMMRemoteDisconnect
	e.cbDisconnect = purego.NewCallback(func(userdata uintptr) {
		e.mu.Lock()
		e.state = stateFailed
		e.cond.Broadcast()
		e.mu.Unlock()
		e.tasks.Range(func(k, v interface{}) bool {
			if ch, ok := v.(chan string); ok {
				select {
				case ch <- "":
				default:
				}
			}
			return true
		})
	})
	e.mojo.SetMMMojoEnvironmentCallbacks(env, kMMRemoteDisconnect, e.cbDisconnect)

	// slot 6 = kMMRemoteProcessLaunched
	e.cbLaunched = purego.NewCallback(func(userdata uintptr) {})
	e.mojo.SetMMMojoEnvironmentCallbacks(env, kMMRemoteProcessLaunched, e.cbLaunched)

	// slot 7 = kMMRemoteProcessLaunchFailed
	e.cbFailed = purego.NewCallback(func(errCode int32, userdata uintptr) {
		e.mu.Lock()
		if e.state == statePending {
			e.state = stateFailed
			e.cond.Broadcast()
		}
		e.mu.Unlock()
	})
	e.mojo.SetMMMojoEnvironmentCallbacks(env, kMMRemoteProcessLaunchFailed, e.cbFailed)

	// slot 8 = kMMRemoteMojoError (nop)
	e.cbError = purego.NewCallback(func(errBuf uintptr, errSize int32, userdata uintptr) {})
	e.mojo.SetMMMojoEnvironmentCallbacks(env, kMMRemoteMojoError, e.cbError)

	// Init params
	e.cstrExePath = cstring(wxocrPath)
	trueVal := uintptr(1) // bool true as uintptr
	e.mojo.SetMMMojoEnvironmentInitParams(env, kMMHostProcess, trueVal)
	e.mojo.SetMMMojoEnvironmentInitParams(env, kMMExePath, uintptr(unsafe.Pointer(&e.cstrExePath[0])))

	// no-sandbox (required on Linux)
	e.cstrNoSandboxKey = cstring("no-sandbox")
	e.cstrNoSandboxVal = cstring("")
	e.mojo.AppendMMSubProcessSwitchNative(env, uintptr(unsafe.Pointer(&e.cstrNoSandboxKey[0])), uintptr(unsafe.Pointer(&e.cstrNoSandboxVal[0])))

	e.mojo.StartMMMojoEnvironment(env)
	return nil
}

// onReadPush is called by the Mojo thread when wxocr sends a message.
func (e *ocrEngine) onReadPush(requestID uint32, pb []byte) {
	switch requestID {
	case reqHandShake:
		// wx4.OCRSupportMessage: field 1 = supported (bool)
		supported := decodeHandshake(pb)
		e.mu.Lock()
		if supported {
			e.state = stateInited
		} else {
			e.state = stateFailed
		}
		e.cond.Broadcast()
		e.mu.Unlock()

	case respOCR:
		// wx4.ParseOCRRespMessage: field 1=task_id, field 2=err_code, field 3=res(OcrResult)
		taskID, jsonStr := decodeOCRResp(pb)
		if v, ok := e.tasks.Load(taskID); ok {
			if ch, ok2 := v.(chan string); ok2 {
				select {
				case ch <- jsonStr:
				default:
				}
			}
		}
	}
}

// waitReady waits for handshake to complete (up to timeoutMs).
func (e *ocrEngine) waitReady(timeoutMs int) bool {
	timer := time.AfterFunc(time.Duration(timeoutMs)*time.Millisecond, func() {
		e.mu.Lock()
		e.cond.Broadcast()
		e.mu.Unlock()
	})
	defer timer.Stop()

	e.mu.Lock()
	defer e.mu.Unlock()
	for e.state == statePending {
		e.cond.Wait()
	}
	return e.state == stateInited
}

// doOCR sends an OCR request and returns the raw JSON response.
func (e *ocrEngine) doOCR(imgPath string) (string, error) {
	if !e.waitReady(5000) {
		return "", fmt.Errorf("pureocr: wxocr not ready (state=%d)", e.state)
	}

	taskID := atomic.AddUint64(&e.taskSeq, 1)
	if taskID == 0 {
		taskID = atomic.AddUint64(&e.taskSeq, 1)
	}

	ch := make(chan string, 1)
	e.tasks.Store(taskID, ch)
	defer e.tasks.Delete(taskID)

	pb := encodeOCRReq(taskID, imgPath)
	writeInfo := e.mojo.CreateMMMojoWriteInfo(kMMPush, false, reqOCR)
	if writeInfo == 0 {
		return "", fmt.Errorf("pureocr: CreateMMMojoWriteInfo failed")
	}
	buf := e.mojo.GetMMMojoWriteInfoRequest(writeInfo, uint64(len(pb)))
	if buf == 0 {
		e.mojo.RemoveMMMojoWriteInfo(writeInfo)
		return "", fmt.Errorf("pureocr: GetMMMojoWriteInfoRequest failed")
	}
	copy(ptrToSlice(buf, len(pb)), pb)
	if !e.mojo.SendMMMojoWriteInfo(e.env, writeInfo) {
		return "", fmt.Errorf("pureocr: SendMMMojoWriteInfo failed")
	}

	select {
	case result := <-ch:
		if result == "" {
			return "", fmt.Errorf("pureocr: engine disconnected during OCR")
		}
		return result, nil
	case <-time.After(10 * time.Second):
		return "", fmt.Errorf("pureocr: OCR timeout")
	}
}

func (e *ocrEngine) stop() {
	if e.env != 0 {
		e.mojo.StopMMMojoEnvironment(e.env)
		e.mojo.RemoveMMMojoEnvironment(e.env)
		e.env = 0
	}
}

// cstring converts a Go string to a null-terminated []byte.
func cstring(s string) []byte {
	b := make([]byte, len(s)+1)
	copy(b, s)
	return b
}

// ── Protobuf helpers (hand-rolled, no external dependency) ──────────────────

func pbTag(field, wireType int) []byte {
	return pbVarint(uint64(field<<3 | wireType))
}

func pbVarint(v uint64) []byte {
	var buf [10]byte
	n := binary.PutUvarint(buf[:], v)
	return buf[:n]
}

func pbBytes(field int, data []byte) []byte {
	out := pbTag(field, 2)
	out = append(out, pbVarint(uint64(len(data)))...)
	return append(out, data...)
}

func pbString(field int, s string) []byte {
	return pbBytes(field, []byte(s))
}

func pbBool(field int, v bool) []byte {
	b := byte(0)
	if v {
		b = 1
	}
	out := pbTag(field, 0)
	return append(out, b)
}

func pbUint64(field int, v uint64) []byte {
	out := pbTag(field, 0)
	return append(out, pbVarint(v)...)
}

// encodeOCRReq encodes wx4.ParseOCRReqMessage:
//
//	message ParseOCRReqMessage {
//	  uint64 task_id = 1;
//	  string pic_path = 2;
//	  uint32 xx3 = 3;    (unused)
//	  uint32 xx4 = 4;    (unused)
//	  bytes  pic_data = 5; (unused — we pass path instead)
//	  ReqType rt = 6;       // { bool t1=1; bool t2=2; bool t3=3; }
//	}
func encodeOCRReq(taskID uint64, picPath string) []byte {
	rt := append(pbBool(1, true), pbBool(2, true)...)
	rt = append(rt, pbBool(3, false)...)

	var msg []byte
	msg = append(msg, pbUint64(1, taskID)...)
	msg = append(msg, pbString(2, picPath)...)
	msg = append(msg, pbBytes(6, rt)...) // rt is field 6, not 3!
	return msg
}

// decodeHandshake decodes wx4.OCRSupportMessage and returns supported bool.
//
//	message OCRSupportMessage { bool supported = 1; }
func decodeHandshake(pb []byte) bool {
	i := 0
	for i < len(pb) {
		tag, n := binary.Uvarint(pb[i:])
		if n <= 0 {
			break
		}
		i += n
		field := tag >> 3
		wire := tag & 0x7
		switch wire {
		case 0: // varint
			val, n2 := binary.Uvarint(pb[i:])
			if n2 <= 0 {
				return false
			}
			i += n2
			if field == 1 {
				return val != 0
			}
		case 2: // length-delimited
			l, n2 := binary.Uvarint(pb[i:])
			if n2 <= 0 {
				return false
			}
			i += n2 + int(l)
		default:
			return false
		}
	}
	return false
}

// ocrResultLine holds one text line from the OCR response.
type ocrResultLine struct {
	Text   string  `json:"text"`
	Rate   float32 `json:"rate"`
	Left   float32 `json:"left"`
	Top    float32 `json:"top"`
	Right  float32 `json:"right"`
	Bottom float32 `json:"bottom"`
}

// decodeOCRResp decodes wx4.ParseOCRRespMessage and returns (taskID, jsonString).
//
//	message ParseOCRRespMessage {
//	  uint64 task_id = 1;
//	  int32  err_code = 2;
//	  OCRResultInfo res = 3;
//	    OCRResultInfo: { repeated OCRResultLine lines = 3; }
//	    OCRResultLine: { string text=2; float rate=3; float left=5; float top=6; float right=7; float bottom=8; }
//	}
func decodeOCRResp(pb []byte) (taskID uint64, jsonStr string) {
	var errCode int32
	var lines []ocrResultLine

	i := 0
	for i < len(pb) {
		tag, n := binary.Uvarint(pb[i:])
		if n <= 0 {
			break
		}
		i += n
		field := int(tag >> 3)
		wire := tag & 0x7

		switch wire {
		case 0: // varint
			val, n2 := binary.Uvarint(pb[i:])
			if n2 <= 0 {
				goto done
			}
			i += n2
			switch field {
			case 1:
				taskID = val
			case 2:
				errCode = int32(val)
			}
		case 2: // length-delimited
			l, n2 := binary.Uvarint(pb[i:])
			if n2 <= 0 {
				goto done
			}
			i += n2
			end := i + int(l)
			if end > len(pb) {
				goto done
			}
			chunk := pb[i:end]
			i = end
			if field == 3 { // OcrResult
				lines = decodeOcrResult(chunk)
			}
		default:
			// skip fixed-size or unknown fields
			if wire == 1 {
				i += 8
			} else if wire == 5 {
				i += 4
			} else {
				goto done
			}
		}
	}
done:
	type jsonResp struct {
		ErrCode     int32           `json:"errcode"`
		OcrResponse []ocrResultLine `json:"ocr_response"`
	}
	b, _ := json.Marshal(jsonResp{ErrCode: errCode, OcrResponse: lines})
	jsonStr = string(b)
	return
}

func decodeOcrResult(pb []byte) []ocrResultLine {
	var lines []ocrResultLine
	i := 0
	for i < len(pb) {
		tag, n := binary.Uvarint(pb[i:])
		if n <= 0 {
			break
		}
		i += n
		field := int(tag >> 3)
		wire := tag & 0x7
		if wire == 2 {
			l, n2 := binary.Uvarint(pb[i:])
			if n2 <= 0 {
				break
			}
			i += n2
			end := i + int(l)
			if end > len(pb) {
				break
			}
			chunk := pb[i:end]
			i = end
			if field == 3 { // OCRResultLine (field 3 in OCRResultInfo)
				lines = append(lines, decodeOCRResultLine(chunk))
			}
		} else if wire == 0 {
			_, n2 := binary.Uvarint(pb[i:])
			if n2 <= 0 {
				break
			}
			i += n2
		} else {
			// wire=1 (64-bit) or wire=5 (32-bit) — skip fixed-size fields
			skip := 0
			if wire == 1 {
				skip = 8
			} else if wire == 5 {
				skip = 4
			} else {
				break
			}
			if i+skip > len(pb) {
				break
			}
			i += skip
		}
	}
	return lines
}

func decodeOCRResultLine(pb []byte) ocrResultLine {
	var l ocrResultLine
	i := 0
	for i < len(pb) {
		tag, n := binary.Uvarint(pb[i:])
		if n <= 0 {
			break
		}
		i += n
		field := int(tag >> 3)
		wire := tag & 0x7
		switch wire {
		case 2: // length-delimited (string, bytes, or sub-message)
			ln, n2 := binary.Uvarint(pb[i:])
			if n2 <= 0 {
				return l
			}
			i += n2
			end := i + int(ln)
			if end > len(pb) {
				return l
			}
			if field == 2 { // text
				l.Text = string(pb[i:end])
			}
			i = end
		case 5: // float32 (fixed32)
			if i+4 > len(pb) {
				return l
			}
			bits := binary.LittleEndian.Uint32(pb[i : i+4])
			f := *(*float32)(unsafe.Pointer(&bits))
			i += 4
			switch field {
			case 3: // rate
				l.Rate = f
			case 5: // left
				l.Left = f
			case 6: // top
				l.Top = f
			case 7: // right
				l.Right = f
			case 8: // bottom
				l.Bottom = f
			}
		case 0: // varint (bool fields like unknown_0)
			_, n2 := binary.Uvarint(pb[i:])
			if n2 <= 0 {
				return l
			}
			i += n2
		default:
			return l
		}
	}
	return l
}
