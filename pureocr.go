//go:build linux && (amd64 || arm64)

// Package pureocr provides on-device OCR via the WeChat OCR engine.
//
// Assets must be present at runtime:
//
//	/opt/ocr/   — libocr.so, libmmmojo.so, wxocr, ocr_model/
//	/opt/glibc/ — glibc runtime (ld-linux, libc, libstdc++, …)
//
// The process must be started through the glibc dynamic linker so that
// dlopen can load the glibc-linked OCR shared libraries from a musl host
// (e.g. Alpine).  A typical entrypoint wrapper looks like:
//
//	exec /opt/glibc/ld-linux-aarch64.so.1 --library-path /opt/glibc /myapp "$@"
package pureocr

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

const ocrDir = "/opt/ocr"

// Block is one recognised text region returned by the OCR engine.
type Block struct {
	Text   string  `json:"text"`
	Rate   float64 `json:"rate"`
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Right  float64 `json:"right"`
	Bottom float64 `json:"bottom"`
}

// Result is the full OCR response for a single image.
type Result struct {
	ErrCode int     `json:"errcode"`
	Blocks  []Block `json:"ocr_response"`
}

// Text returns all recognised text joined by newlines.
func (r Result) Text() string {
	parts := make([]string, 0, len(r.Blocks))
	for _, b := range r.Blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

var (
	once      sync.Once
	initErr   error
	mu        sync.Mutex
	fnOCR     func(exe, dir, img string, cb uintptr) bool
	fnStopOCR func()
)

func load() error {
	once.Do(func() {
		_ = os.Setenv("LD_LIBRARY_PATH", ocrDir+":"+os.Getenv("LD_LIBRARY_PATH"))
		lib, err := purego.Dlopen(ocrDir+"/libocr.so", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			initErr = fmt.Errorf("pureocr: dlopen libocr.so: %w", err)
			return
		}
		purego.RegisterLibFunc(&fnOCR, lib, "wechat_ocr")
		purego.RegisterLibFunc(&fnStopOCR, lib, "stop_ocr")
	})
	return initErr
}

// Stop shuts down the OCR engine and releases resources.
// It is safe to call Stop multiple times.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if fnStopOCR != nil {
		fnStopOCR()
	}
}

// OCRFile runs OCR on the image at the given path and returns the result.
func OCRFile(imagePath string) (Result, error) {
	if err := load(); err != nil {
		return Result{}, err
	}
	ch := make(chan string, 1)
	cb := purego.NewCallback(func(p *byte) { ch <- cStr(p) })
	mu.Lock()
	ok := fnOCR(ocrDir+"/wxocr", ocrDir, imagePath, cb)
	mu.Unlock()
	if !ok {
		return Result{}, fmt.Errorf("pureocr: ocr engine returned false")
	}
	var r Result
	if err := json.Unmarshal([]byte(<-ch), &r); err != nil {
		return Result{}, fmt.Errorf("pureocr: parse response: %w", err)
	}
	if r.ErrCode != 0 {
		return Result{}, fmt.Errorf("pureocr: errcode %d", r.ErrCode)
	}
	return r, nil
}

// OCRBytes runs OCR on raw image bytes.
// The bytes are written to a temporary file which is removed after the call.
func OCRBytes(data []byte) (Result, error) {
	tmp, err := os.CreateTemp("", "pureocr-*.img")
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return Result{}, err
	}
	tmp.Close()
	return OCRFile(tmp.Name())
}

func cStr(p *byte) string {
	if p == nil {
		return ""
	}
	// Walk until NUL (bounded to 1 MiB for safety).
	s := unsafe.Slice(p, 1<<20)
	n := 0
	for n < len(s) && s[n] != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}
