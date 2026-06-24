//go:build linux && (amd64 || arm64)

// Package pureocr provides on-device OCR via the WeChat OCR engine.
//
// OCR assets (libocr.so, libmmmojo.so, wxocr, ocr_model/) are embedded in the
// binary via //go:embed and extracted to a temporary directory at startup.
// No external installation or /opt/ocr/ directory is required.
//
// Supported platforms: linux/amd64, linux/arm64.
//
// On musl-based systems (e.g. Alpine) the process must be started through
// the glibc dynamic linker so that dlopen can load the glibc-linked OCR
// shared libraries.  A typical Alpine entrypoint wrapper looks like:
//
//	exec /opt/glibc/ld-linux-aarch64.so.1 --library-path /opt/glibc /myapp "$@"
package pureocr

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

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
	ocrDir    string
	fnOCR     func(exe, dir, img string, cb uintptr) bool
	fnStopOCR func()
)

func load() error {
	once.Do(func() {
		var ok bool
		d, err := os.MkdirTemp("", "pureocr-*")
		if err != nil {
			initErr = fmt.Errorf("pureocr: mkdirtemp: %w", err)
			return
		}
		ocrDir = d
		defer func() {
			if !ok {
				os.RemoveAll(ocrDir)
				ocrDir = ""
			}
		}()

		for _, name := range []string{"libocr.so", "libmmmojo.so", "wxocr"} {
			data, err := ocrFS.ReadFile(archPrefix + "/" + name)
			if err != nil {
				initErr = fmt.Errorf("pureocr: read %s: %w", name, err)
				return
			}
			if err := os.WriteFile(filepath.Join(ocrDir, name), data, 0755); err != nil {
				initErr = fmt.Errorf("pureocr: write %s: %w", name, err)
				return
			}
		}

		if err := extractDir(ocrFS, "assets/ocr_model", filepath.Join(ocrDir, "ocr_model")); err != nil {
			initErr = fmt.Errorf("pureocr: extract model: %w", err)
			return
		}

		_ = os.Setenv("LD_LIBRARY_PATH", ocrDir+":"+os.Getenv("LD_LIBRARY_PATH"))
		lib, err := purego.Dlopen(filepath.Join(ocrDir, "libocr.so"), purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			initErr = fmt.Errorf("pureocr: dlopen libocr.so: %w", err)
			return
		}
		purego.RegisterLibFunc(&fnOCR, lib, "wechat_ocr")
		purego.RegisterLibFunc(&fnStopOCR, lib, "stop_ocr")

		ok = true
	})
	return initErr
}

// Stop shuts down the OCR engine and releases resources.
// It is safe to call Stop multiple times. After Stop the package must not
// be used again (the temp directory is removed).
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if fnStopOCR != nil {
		fnStopOCR()
	}
	if ocrDir != "" {
		os.RemoveAll(ocrDir)
		ocrDir = ""
	}
}

// OCRFile runs OCR on the image at the given path and returns the result.
func OCRFile(imagePath string) (result Result, err error) {
	if err := load(); err != nil {
		return Result{}, err
	}

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("pureocr: panic: %v", r)
		}
	}()

	mu.Lock()
	defer mu.Unlock()

	ch := make(chan string, 1)
	cb := purego.NewCallback(func(p *byte) { ch <- cStr(p) })

	if ok := fnOCR(filepath.Join(ocrDir, "wxocr"), ocrDir, imagePath, cb); !ok {
		return Result{}, fmt.Errorf("pureocr: ocr engine returned false")
	}

	select {
	case raw := <-ch:
		var r Result
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return Result{}, fmt.Errorf("pureocr: parse response: %w", err)
		}
		if r.ErrCode != 0 {
			return Result{}, fmt.Errorf("pureocr: errcode %d", r.ErrCode)
		}
		return r, nil
	case <-time.After(10 * time.Second):
		return Result{}, fmt.Errorf("pureocr: ocr timed out after 10s")
	}
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

// extractDir copies all files from an embedded directory (src) into a local
// directory (dst), preserving the relative tree structure.
func extractDir(fsys fs.FS, src, dst string) error {
	return fs.WalkDir(fsys, src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0755)
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0644)
	})
}
