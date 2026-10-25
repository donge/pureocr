package pureocr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	ort "github.com/yalue/onnxruntime_go"
)

// Model file names inside the embedded bundle / ModelDir.
const (
	fileDet    = "det.onnx"
	fileRec    = "rec.onnx"
	fileRecCJK = "rec_cjk.onnx"
	fileScript = "script.onnx"
	fileOrient = "orient.onnx"
	fileMeta   = "meta.json"
)

// meta mirrors the parts of meta.json this package uses.
type meta struct {
	RecHeight   int             `json:"rec_height"`
	RecWindow   int             `json:"rec_window"`
	DetLongSide int             `json:"det_long_side"`
	Vocab       json.RawMessage `json:"vocab"`
	VocabCJK    json.RawMessage `json:"vocab_cjk"`
}

// parseVocab reads a vocab entry: meta.json stores each vocab as a JSON string
// that itself holds {"blank":0,"chars":[...]}.
func parseVocab(raw json.RawMessage) ([]string, error) {
	body := raw
	if len(body) > 0 && body[0] == '"' {
		var s string
		if err := json.Unmarshal(body, &s); err != nil {
			return nil, err
		}
		body = []byte(s)
	}
	var v struct {
		Chars []string `json:"chars"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, err
	}
	if len(v.Chars) == 0 {
		return nil, errors.New("pureocr: empty vocabulary")
	}
	return v.Chars, nil
}

// session is one ONNX Runtime session plus the vocabulary it decodes with.
type session struct {
	s      *ort.DynamicAdvancedSession
	vocab  []string
	height int // recognizer input height in pixels
	name   string
}

type models struct {
	det    *ort.DynamicAdvancedSession
	rec    *session
	cjk    *session
	script *session
	orient *ort.DynamicAdvancedSession
	meta   meta
}

func newSession(data []byte, in, out []string, threads int) (*ort.DynamicAdvancedSession, error) {
	opts, err := ort.NewSessionOptions()
	if err != nil {
		return nil, err
	}
	defer opts.Destroy()
	if err := opts.SetIntraOpNumThreads(threads); err != nil {
		return nil, err
	}
	if err := opts.SetInterOpNumThreads(1); err != nil {
		return nil, err
	}
	if err := opts.SetGraphOptimizationLevel(ort.GraphOptimizationLevelEnableAll); err != nil {
		return nil, err
	}
	// Line batches change shape on every call; the CPU arena only grows.
	if err := opts.SetCpuMemArena(false); err != nil {
		return nil, err
	}
	return ort.NewDynamicAdvancedSessionWithONNXData(data, in, out, opts)
}

// openModels loads the five sessions from dir, or from the embedded copies when
// dir is empty.
func openModels(dir string, threads int, charset Charset) (*models, error) {
	read := func(name string) ([]byte, error) {
		if dir != "" {
			return os.ReadFile(filepath.Join(dir, name))
		}
		data, err := embeddedModel(name)
		if err != nil {
			return nil, err
		}
		return data, nil
	}

	metaRaw, err := read(fileMeta)
	if err != nil {
		return nil, fmt.Errorf("pureocr: read %s: %w", fileMeta, err)
	}
	m := &models{}
	if err := json.Unmarshal(metaRaw, &m.meta); err != nil {
		return nil, fmt.Errorf("pureocr: parse meta.json: %w", err)
	}
	baseVocab, err := parseVocab(m.meta.Vocab)
	if err != nil {
		return nil, err
	}
	cjkVocab, err := parseVocab(m.meta.VocabCJK)
	if err != nil {
		return nil, err
	}

	det, err := read(fileDet)
	if err != nil {
		return nil, err
	}
	if m.det, err = newSession(det, []string{"x"}, []string{"out"}, threads); err != nil {
		return nil, fmt.Errorf("pureocr: load %s: %w", fileDet, err)
	}

	if charset == CharsetMixed {
		rec, err := read(fileRec)
		if err != nil {
			return nil, err
		}
		recS, err := newSession(rec, []string{"x", "widths"}, []string{"ids", "conf", "lengths"}, threads)
		if err != nil {
			return nil, fmt.Errorf("pureocr: load %s: %w", fileRec, err)
		}
		window := m.meta.RecWindow
		if window <= 0 {
			window = 1
		}
		m.rec = &session{s: recS, vocab: baseVocab, height: m.meta.RecHeight * window, name: "base"}
	}

	cjk, err := read(fileRecCJK)
	if err != nil {
		return nil, err
	}
	cjkS, err := newSession(cjk, []string{"x", "widths"}, []string{"ids", "conf", "lengths"}, threads)
	if err != nil {
		return nil, fmt.Errorf("pureocr: load %s: %w", fileRecCJK, err)
	}
	m.cjk = &session{s: cjkS, vocab: cjkVocab, height: m.meta.RecHeight, name: "cjk"}

	if charset == CharsetMixed {
		script, err := read(fileScript)
		if err != nil {
			return nil, err
		}
		scriptS, err := newSession(script, []string{"x", "widths"}, []string{"p"}, threads)
		if err != nil {
			return nil, fmt.Errorf("pureocr: load %s: %w", fileScript, err)
		}
		m.script = &session{s: scriptS, height: m.meta.RecHeight, name: "script"}
	}

	// The orientation model is optional: without it pages are assumed upright.
	if orient, oerr := read(fileOrient); oerr == nil {
		if m.orient, err = newSession(orient, []string{"x"}, []string{"p"}, threads); err != nil {
			return nil, fmt.Errorf("pureocr: load %s: %w", fileOrient, err)
		}
	}
	return m, nil
}

func (m *models) close() error {
	var first error
	closeS := func(s *ort.DynamicAdvancedSession) {
		if s != nil {
			if err := s.Destroy(); err != nil && first == nil {
				first = err
			}
		}
	}
	closeS(m.det)
	closeS(m.orient)
	if m.rec != nil {
		closeS(m.rec.s)
	}
	if m.cjk != nil {
		closeS(m.cjk.s)
	}
	if m.script != nil {
		closeS(m.script.s)
	}
	return first
}

// run recognises a batch of ink maps with one recognizer.
func (s *session) run(in *batchInput) ([]string, []float64, error) {
	xT, err := ort.NewTensor(ort.NewShape(int64(in.n), 1, int64(in.h), int64(in.w)), in.data)
	if err != nil {
		return nil, nil, err
	}
	defer xT.Destroy()
	wT, err := ort.NewTensor(ort.NewShape(int64(in.n)), in.widths)
	if err != nil {
		return nil, nil, err
	}
	defer wT.Destroy()

	out := []ort.Value{nil, nil, nil}
	if err := s.s.Run([]ort.Value{xT, wT}, out); err != nil {
		return nil, nil, err
	}
	ids := out[0].(*ort.Tensor[int64])
	conf := out[1].(*ort.Tensor[float32])
	lens := out[2].(*ort.Tensor[int64])
	defer ids.Destroy()
	defer conf.Destroy()
	defer lens.Destroy()

	idData, cData, lData := ids.GetData(), conf.GetData(), lens.GetData()
	T := int(ids.GetShape()[1])

	texts := make([]string, in.n)
	confs := make([]float64, in.n)
	var sb []byte
	for k := 0; k < in.n; k++ {
		n := min(int(lData[k]), T)
		sb = sb[:0]
		var sum float64
		var cnt int
		prev := int64(-1)
		for t := 0; t < n; t++ {
			id := idData[k*T+t]
			if id == 0 || id == prev {
				prev = id
				continue
			}
			prev = id
			if int(id) <= len(s.vocab) {
				sb = append(sb, s.vocab[id-1]...)
			}
			sum += float64(cData[k*T+t])
			cnt++
		}
		texts[k] = string(sb)
		if cnt > 0 {
			confs[k] = sum / float64(cnt)
		}
	}
	return texts, confs, nil
}
