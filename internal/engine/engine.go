//go:build linux && arm64

package engine

// Engine is the Go reimplementation of CWeChatOCR.
//
// Protocol (wx4 / Linux):
//  1. Init:  LoadMojo → InitializeMMMojo
//  2. Start: CreateMMMojoEnvironment → SetMMMojoEnvironmentCallbacks × 8
//            → SetMMMojoEnvironmentInitParams(kMMHostProcess, kMMExePath)
//            → AppendMMSubProcessSwitchNative("no-sandbox", "")
//            → StartMMMojoEnvironment  (forks wxocr via execvp)
//  3. Handshake: wxocr sends HAND_SHAKE(10001) → OCRSupportMessage{supported}
//  4. OCR: CreateMMMojoWriteInfo(kMMPush, false, 10010)
//          → GetMMMojoWriteInfoRequest → fill protobuf → SendMMMojoWriteInfo
//  5. Response: ReadPush with requestID=10011 → decodeOCRResp

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

// Result is the OCR response returned by OCRFile.
type Result struct {
	ErrCode int32       `json:"errcode"`
	Blocks  []BlockLine `json:"ocr_response"`
}

// BlockLine is one recognised text region.
type BlockLine struct {
	Text   string  `json:"text"`
	Rate   float32 `json:"rate"`
	Left   float32 `json:"left"`
	Top    float32 `json:"top"`
	Right  float32 `json:"right"`
	Bottom float32 `json:"bottom"`
}

type engineState int32

const (
	statePending engineState = 0
	stateInited  engineState = 1
	stateFailed  engineState = -1
)

// Engine manages the wxocr subprocess via libmmmojo.so.
type Engine struct {
	mojo   *MojoLib
	env    uintptr
	exeDir string

	mu    sync.Mutex
	cond  *sync.Cond
	state engineState

	taskSeq uint64
	tasks   sync.Map // uint64 → chan string

	// purego callback handles — must stay alive for the process lifetime
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

// New creates and starts the OCR engine.
// wxocrPath is the absolute path to the wxocr executable.
// exeDir is the directory that also contains libmmmojo.so and ocr_model/.
func New(mojo *MojoLib, wxocrPath, exeDir string) (*Engine, error) {
	e := &Engine{mojo: mojo, exeDir: exeDir, state: statePending}
	e.cond = sync.NewCond(&e.mu)
	if err := e.start(wxocrPath); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) start(wxocrPath string) error {
	env := e.mojo.CreateMMMojoEnvironment()
	if env == 0 {
		return fmt.Errorf("CreateMMMojoEnvironment returned nil")
	}
	e.env = env

	// slot 1 = kMMReadPush
	e.cbReadPush = purego.NewCallback(func(requestID uint32, readInfo uintptr, _ uintptr) {
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
	e.cbNop1 = purego.NewCallback(func(_ uint32, readInfo uintptr, _ uintptr) {
		e.mojo.RemoveMMMojoReadInfo(readInfo)
	})
	e.mojo.SetMMMojoEnvironmentCallbacks(env, kMMReadPull, e.cbNop1)

	// slot 3 = kMMReadShared (nop)
	e.cbNop2 = purego.NewCallback(func(_ uint32, readInfo uintptr, _ uintptr) {
		e.mojo.RemoveMMMojoReadInfo(readInfo)
	})
	e.mojo.SetMMMojoEnvironmentCallbacks(env, kMMReadShared, e.cbNop2)

	// slot 4 = kMMRemoteConnect
	e.cbConnect = purego.NewCallback(func(isConnected bool, _ uintptr) {
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
	e.cbDisconnect = purego.NewCallback(func(_ uintptr) {
		e.mu.Lock()
		e.state = stateFailed
		e.cond.Broadcast()
		e.mu.Unlock()
		e.tasks.Range(func(_, v interface{}) bool {
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

	// slot 6 = kMMRemoteProcessLaunched (nop)
	e.cbLaunched = purego.NewCallback(func(_ uintptr) {})
	e.mojo.SetMMMojoEnvironmentCallbacks(env, kMMRemoteProcessLaunched, e.cbLaunched)

	// slot 7 = kMMRemoteProcessLaunchFailed
	e.cbFailed = purego.NewCallback(func(_ int32, _ uintptr) {
		e.mu.Lock()
		if e.state == statePending {
			e.state = stateFailed
			e.cond.Broadcast()
		}
		e.mu.Unlock()
	})
	e.mojo.SetMMMojoEnvironmentCallbacks(env, kMMRemoteProcessLaunchFailed, e.cbFailed)

	// slot 8 = kMMRemoteMojoError (nop)
	e.cbError = purego.NewCallback(func(_ uintptr, _ int32, _ uintptr) {})
	e.mojo.SetMMMojoEnvironmentCallbacks(env, kMMRemoteMojoError, e.cbError)

	// Init params
	e.cstrExePath = cstring(wxocrPath)
	trueVal := uintptr(1)
	e.mojo.SetMMMojoEnvironmentInitParams(env, kMMHostProcess, trueVal)
	e.mojo.SetMMMojoEnvironmentInitParams(env, kMMExePath, uintptr(unsafe.Pointer(&e.cstrExePath[0])))

	// no-sandbox (required on Linux)
	e.cstrNoSandboxKey = cstring("no-sandbox")
	e.cstrNoSandboxVal = cstring("")
	e.mojo.AppendMMSubProcessSwitchNative(env,
		uintptr(unsafe.Pointer(&e.cstrNoSandboxKey[0])),
		uintptr(unsafe.Pointer(&e.cstrNoSandboxVal[0])))

	e.mojo.StartMMMojoEnvironment(env)
	return nil
}

func (e *Engine) onReadPush(requestID uint32, pb []byte) {
	switch requestID {
	case ReqHandShake:
		supported := decodeHandshake(pb)
		e.mu.Lock()
		if supported {
			e.state = stateInited
		} else {
			e.state = stateFailed
		}
		e.cond.Broadcast()
		e.mu.Unlock()

	case RespOCR:
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

func (e *Engine) waitReady(timeoutMs int) bool {
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

// OCRFile runs OCR on the image at imgPath and returns a Result.
func (e *Engine) OCRFile(imgPath string) (Result, error) {
	if !e.waitReady(5000) {
		return Result{}, fmt.Errorf("wxocr not ready (state=%d)", e.state)
	}

	taskID := atomic.AddUint64(&e.taskSeq, 1)
	if taskID == 0 {
		taskID = atomic.AddUint64(&e.taskSeq, 1)
	}

	ch := make(chan string, 1)
	e.tasks.Store(taskID, ch)
	defer e.tasks.Delete(taskID)

	pb := encodeOCRReq(taskID, imgPath)
	writeInfo := e.mojo.CreateMMMojoWriteInfo(kMMPush, false, ReqOCR)
	if writeInfo == 0 {
		return Result{}, fmt.Errorf("CreateMMMojoWriteInfo failed")
	}
	buf := e.mojo.GetMMMojoWriteInfoRequest(writeInfo, uint64(len(pb)))
	if buf == 0 {
		e.mojo.RemoveMMMojoWriteInfo(writeInfo)
		return Result{}, fmt.Errorf("GetMMMojoWriteInfoRequest failed")
	}
	copy(ptrToSlice(buf, len(pb)), pb)
	if !e.mojo.SendMMMojoWriteInfo(e.env, writeInfo) {
		return Result{}, fmt.Errorf("SendMMMojoWriteInfo failed")
	}

	select {
	case raw := <-ch:
		if raw == "" {
			return Result{}, fmt.Errorf("engine disconnected during OCR")
		}
		var r Result
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return Result{}, fmt.Errorf("parse response: %w (raw: %s)", err, raw)
		}
		return r, nil
	case <-time.After(10 * time.Second):
		return Result{}, fmt.Errorf("OCR timeout")
	}
}

// Stop shuts down the engine.
func (e *Engine) Stop() {
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

// ── Protobuf helpers ─────────────────────────────────────────────────────────

func pbTag(field, wireType int) []byte  { return pbVarint(uint64(field<<3 | wireType)) }
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
func pbString(field int, s string) []byte { return pbBytes(field, []byte(s)) }
func pbBool(field int, v bool) []byte {
	b := byte(0)
	if v {
		b = 1
	}
	return append(pbTag(field, 0), b)
}
func pbUint64(field int, v uint64) []byte { return append(pbTag(field, 0), pbVarint(v)...) }

// encodeOCRReq encodes wx4.ParseOCRReqMessage.
//
//	message ParseOCRReqMessage {
//	  uint64  task_id  = 1;
//	  string  pic_path = 2;
//	  ReqType rt       = 6;   // { bool t1=1; bool t2=2; bool t3=3; }
//	}
func encodeOCRReq(taskID uint64, picPath string) []byte {
	rt := append(pbBool(1, true), pbBool(2, true)...)
	rt = append(rt, pbBool(3, false)...)
	var msg []byte
	msg = append(msg, pbUint64(1, taskID)...)
	msg = append(msg, pbString(2, picPath)...)
	msg = append(msg, pbBytes(6, rt)...)
	return msg
}

// decodeHandshake decodes wx4.OCRSupportMessage { bool supported = 1; }
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
		if wire == 0 {
			val, n2 := binary.Uvarint(pb[i:])
			if n2 <= 0 {
				return false
			}
			i += n2
			if field == 1 {
				return val != 0
			}
		} else if wire == 2 {
			l, n2 := binary.Uvarint(pb[i:])
			if n2 <= 0 {
				return false
			}
			i += n2 + int(l)
		} else {
			return false
		}
	}
	return false
}

// decodeOCRResp decodes wx4.ParseOCRRespMessage.
//
//	message ParseOCRRespMessage {
//	  uint64        task_id = 1;
//	  int32         err_code = 2;
//	  OCRResultInfo res = 3;
//	}
//	message OCRResultInfo { repeated OCRResultLine lines = 3; }
//	message OCRResultLine {
//	  string text=2; float rate=3; float left=5; float top=6; float right=7; float bottom=8;
//	}
func decodeOCRResp(pb []byte) (taskID uint64, jsonStr string) {
	var errCode int32
	var lines []BlockLine
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
		case 0:
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
		case 2:
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
			if field == 3 {
				lines = decodeOcrResult(chunk)
			}
		default:
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
	type resp struct {
		ErrCode int32       `json:"errcode"`
		Blocks  []BlockLine `json:"ocr_response"`
	}
	b, _ := json.Marshal(resp{ErrCode: errCode, Blocks: lines})
	jsonStr = string(b)
	return
}

func decodeOcrResult(pb []byte) []BlockLine {
	var lines []BlockLine
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
		case 2:
			l, n2 := binary.Uvarint(pb[i:])
			if n2 <= 0 {
				return lines
			}
			i += n2
			end := i + int(l)
			if end > len(pb) {
				return lines
			}
			chunk := pb[i:end]
			i = end
			if field == 3 { // OCRResultLine
				lines = append(lines, decodeOCRResultLine(chunk))
			}
		case 0:
			_, n2 := binary.Uvarint(pb[i:])
			if n2 <= 0 {
				return lines
			}
			i += n2
		default:
			if wire == 1 {
				if i+8 > len(pb) {
					return lines
				}
				i += 8
			} else if wire == 5 {
				if i+4 > len(pb) {
					return lines
				}
				i += 4
			} else {
				return lines
			}
		}
	}
	return lines
}

func decodeOCRResultLine(pb []byte) BlockLine {
	var l BlockLine
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
		case 2: // length-delimited (string or sub-message)
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
			case 3:
				l.Rate = f
			case 5:
				l.Left = f
			case 6:
				l.Top = f
			case 7:
				l.Right = f
			case 8:
				l.Bottom = f
			}
		case 0:
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
