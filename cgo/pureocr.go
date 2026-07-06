//go:build linux && (amd64 || arm64)

// Package cgo provides on-device OCR via the WeChat OCR engine.
//
// It uses CGo to statically link the OCR C++ library (libwcocr.a +
// libprotobuf-lite.a), removing the need for libocr.so at runtime.
// libmmmojo.so is still loaded via dlopen at runtime.
//
// Only requires GLIBC_2.17 — compatible with older systems such as
// KylinOS 10 (glibc 2.28).
//
// Build with CGO_ENABLED=1 and a C++20-capable compiler (GCC >= 10).
package cgo

/*
#cgo amd64 LDFLAGS: -L${SRCDIR}/assets/amd64 -lwcocr -lprotobuf-lite
#cgo arm64 LDFLAGS: -L${SRCDIR}/assets/arm64 -lwcocr -lprotobuf-lite
#cgo LDFLAGS: -lpthread -Wl,-Bstatic -lstdc++ -Wl,-Bdynamic -lm -ldl

#include <stdlib.h>
#include <stdbool.h>

extern bool wechat_ocr(const char* ocr_exe, const char* wechat_dir, const char* imgfn, void (*set_res)(char*));
extern void stop_ocr();
extern void goOcrCallback(char* result);

static bool do_ocr(const char* exe, const char* dir, const char* img) {
    return wechat_ocr(exe, dir, img, goOcrCallback);
}
*/
import "C"

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
	ocrDir    string
	ocrResult string
	ocrMu     sync.Mutex
	ocrDone   = make(chan struct{}, 1)
	mu        sync.Mutex
)

//export goOcrCallback
func goOcrCallback(result *C.char) {
	ocrMu.Lock()
	ocrResult = C.GoString(result)
	ocrMu.Unlock()
	select {
	case ocrDone <- struct{}{}:
	default:
	}
}

func load() error {
	once.Do(func() {
		var ok bool
		ocrDir = filepath.Join(os.TempDir(), "pureocr-cgo")
		if err := os.RemoveAll(ocrDir); err != nil {
			initErr = fmt.Errorf("pureocr/cgo: cleanup: %w", err)
			return
		}
		if err := os.MkdirAll(ocrDir, 0755); err != nil {
			initErr = fmt.Errorf("pureocr/cgo: mkdir: %w", err)
			return
		}
		defer func() {
			if !ok {
				os.RemoveAll(ocrDir)
				ocrDir = ""
			}
		}()

		for _, name := range []string{"libmmmojo.so", "wxocr"} {
			data, err := cgoFS.ReadFile(archPrefix + "/" + name)
			if err != nil {
				initErr = fmt.Errorf("pureocr/cgo: read %s: %w", name, err)
				return
			}
			if err := os.WriteFile(filepath.Join(ocrDir, name), data, 0755); err != nil {
				initErr = fmt.Errorf("pureocr/cgo: write %s: %w", name, err)
				return
			}
		}

		if err := extractDir(cgoFS, "assets/ocr_model", filepath.Join(ocrDir, "ocr_model")); err != nil {
			initErr = fmt.Errorf("pureocr/cgo: extract model: %w", err)
			return
		}

		_ = os.Setenv("LD_LIBRARY_PATH", ocrDir+":"+os.Getenv("LD_LIBRARY_PATH"))
		ok = true
	})
	return initErr
}

// Stop shuts down the OCR engine and releases resources.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if ocrDir != "" {
		C.stop_ocr()
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
			err = fmt.Errorf("pureocr/cgo: panic: %v", r)
		}
	}()

	mu.Lock()
	defer mu.Unlock()

	select {
	case <-ocrDone:
	default:
	}

	wxocr := filepath.Join(ocrDir, "wxocr")
	cExe := C.CString(wxocr)
	cDir := C.CString(ocrDir)
	cImg := C.CString(imagePath)
	defer C.free(unsafe.Pointer(cExe))
	defer C.free(unsafe.Pointer(cDir))
	defer C.free(unsafe.Pointer(cImg))

	if ok := C.do_ocr(cExe, cDir, cImg); !ok {
		return Result{}, fmt.Errorf("pureocr/cgo: ocr call failed")
	}

	select {
	case <-ocrDone:
		ocrMu.Lock()
		res := ocrResult
		ocrMu.Unlock()
		var r Result
		if err := json.Unmarshal([]byte(res), &r); err != nil {
			return Result{}, fmt.Errorf("pureocr/cgo: parse response: %w", err)
		}
		if r.ErrCode != 0 {
			return Result{}, fmt.Errorf("pureocr/cgo: errcode %d", r.ErrCode)
		}
		return r, nil
	case <-time.After(10 * time.Second):
		return Result{}, fmt.Errorf("pureocr/cgo: ocr timed out after 10s")
	}
}

// OCRBytes runs OCR on raw image bytes.
func OCRBytes(data []byte) (Result, error) {
	tmp, err := os.CreateTemp("", "pureocr-cgo-*.img")
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
