// Package pureocr provides a pure-Go (zero CGo) OCR binding using WeChat's
// on-device OCR engine. All required runtime files are embedded — no external
// installation needed.
//
// Supported platforms: linux/amd64, linux/arm64.
//
// # Usage
//
//	result, err := pureocr.OCRFile("/path/to/image.png")
//	fmt.Println(result.Text())

//go:build linux && (amd64 || arm64)

package pureocr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// Result is the full response from the OCR engine.
type Result struct {
	ErrCode  int     `json:"errcode"`
	Blocks   []Block `json:"ocr_response"`
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
	initErr     error
	mu          sync.Mutex
	tmpDir      string
	ocrExe      string
	ocrDir      string
	fnWechatOCR func(ocrExe, wechatDir, imgPath string, cb uintptr) bool
	fnStopOCR   func()
)

func init() {
	dir, err := os.MkdirTemp("", "pureocr-*")
	if err != nil {
		initErr = fmt.Errorf("pureocr: mkdirtemp: %w", err)
		return
	}
	tmpDir = dir

	// libocr.so — exports wechat_ocr / stop_ocr; references libpureocr.so.
	libocrPath := filepath.Join(tmpDir, "libocr.so")
	if err := os.WriteFile(libocrPath, libocrData, 0755); err != nil {
		initErr = fmt.Errorf("pureocr: write libocr.so: %w", err)
		return
	}

	// libpureocr.so — the Mojo IPC library (= libmmmojo.so).
	// Written under two names: libpureocr.so (dlopen'd by libocr.so) and
	// libmmmojo.so (ELF DT_NEEDED of the wxocr binary).
	libpureocrPath := filepath.Join(tmpDir, "libpureocr.so")
	if err := os.WriteFile(libpureocrPath, libpureocrData, 0755); err != nil {
		initErr = fmt.Errorf("pureocr: write libpureocr.so: %w", err)
		return
	}
	libmmmojoPath := filepath.Join(tmpDir, "libmmmojo.so")
	if err := os.Link(libpureocrPath, libmmmojoPath); err != nil {
		if err = os.WriteFile(libmmmojoPath, libpureocrData, 0755); err != nil {
			initErr = fmt.Errorf("pureocr: write libmmmojo.so: %w", err)
			return
		}
	}

	// wxocr — subprocess launched by libpureocr.so via execvp; name is fixed.
	wxocrPath := filepath.Join(tmpDir, "wxocr")
	if err := os.WriteFile(wxocrPath, wxocrData, 0755); err != nil {
		initErr = fmt.Errorf("pureocr: write wxocr: %w", err)
		return
	}

	// ocr_model/ — model files referenced by wxocr at startup.
	if err := extractFS(ocrModelFS, "embed/ocr_model", filepath.Join(tmpDir, "ocr_model")); err != nil {
		initErr = fmt.Errorf("pureocr: extract ocr_model: %w", err)
		return
	}

	ocrExe = wxocrPath
	ocrDir = tmpDir

	// Prepend tmpDir so libocr.so can find libpureocr.so / libmmmojo.so.
	_ = os.Setenv("LD_LIBRARY_PATH", tmpDir+":"+os.Getenv("LD_LIBRARY_PATH"))

	lib, err := purego.Dlopen(libocrPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		initErr = fmt.Errorf("pureocr: dlopen: %w", err)
		return
	}
	purego.RegisterLibFunc(&fnWechatOCR, lib, "wechat_ocr")
	purego.RegisterLibFunc(&fnStopOCR, lib, "stop_ocr")
}

// Stop shuts down the OCR engine and cleans up extracted temp files.
// Safe to call multiple times.
func Stop() {
	if initErr != nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	fnStopOCR()
	if tmpDir != "" {
		os.RemoveAll(tmpDir)
		tmpDir = ""
	}
}

// cStr converts a null-terminated C string pointer to a Go string.
func cStr(ptr *byte) string {
	if ptr == nil {
		return ""
	}
	b := unsafe.Slice(ptr, 1<<20)
	if n := bytes.IndexByte(b, 0); n >= 0 {
		b = b[:n]
	}
	return string(b)
}

// OCRFile runs OCR on the image at the given path.
func OCRFile(imagePath string) (Result, error) {
	if initErr != nil {
		return Result{}, initErr
	}

	ch := make(chan string, 1)
	cb := purego.NewCallback(func(p *byte) { ch <- cStr(p) })

	mu.Lock()
	ok := fnWechatOCR(ocrExe, ocrDir, imagePath, cb)
	mu.Unlock()

	if !ok {
		return Result{}, fmt.Errorf("pureocr: wechat_ocr returned false")
	}

	raw := <-ch
	var r Result
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return Result{}, fmt.Errorf("pureocr: parse response: %w (raw: %s)", err, raw)
	}
	if r.ErrCode != 0 {
		return Result{}, fmt.Errorf("pureocr: errcode %d", r.ErrCode)
	}
	return r, nil
}

// OCRBytes runs OCR on raw image bytes.
func OCRBytes(data []byte) (Result, error) {
	tmp, err := os.CreateTemp("", "pureocr-img-*")
	if err != nil {
		return Result{}, fmt.Errorf("pureocr: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return Result{}, fmt.Errorf("pureocr: %w", err)
	}
	tmp.Close()
	return OCRFile(tmp.Name())
}

// extractFS extracts all files from fsys under fsRoot into destDir.
func extractFS(fsys fs.FS, fsRoot, destDir string) error {
	return fs.WalkDir(fsys, fsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(fsRoot, path)
		dest := filepath.Join(destDir, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0755)
		}
		f, err := fsys.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		data, err := io.ReadAll(f)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, data, 0644)
	})
}
