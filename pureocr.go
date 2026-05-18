// Package pureocr provides on-device OCR via the WeChat OCR engine.
// On linux/arm64 it spawns an ocr_helper subprocess (glibc) that communicates
// over stdin/stdout JSON pipes, so that the musl-linked tequila binary can use
// the glibc-only libmmmojo.so without any libc conflicts.
//
// # Usage
//
//	result, err := pureocr.OCRFile("/path/to/image.png")
//	fmt.Println(result.Text())

//go:build linux && arm64

package pureocr

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	initErr  error
	initOnce sync.Once
	mu       sync.Mutex
	tmpDir   string
	helper   *helperProc
)

func ensureInit() error {
	initOnce.Do(func() {
		initErr = doInit()
	})
	return initErr
}

func doInit() error {
	dir, err := os.MkdirTemp("", "pureocr-*")
	if err != nil {
		return fmt.Errorf("pureocr: mkdirtemp: %w", err)
	}
	tmpDir = dir

	// Extract libmmmojo.so (= libpureocr.so).
	libPath := filepath.Join(tmpDir, "libmmmojo.so")
	if err := os.WriteFile(libPath, libpureocrData, 0755); err != nil {
		return fmt.Errorf("pureocr: write libmmmojo.so: %w", err)
	}

	// Extract wxocr executable.
	wxocrPath := filepath.Join(tmpDir, "wxocr")
	if err := os.WriteFile(wxocrPath, wxocrData, 0755); err != nil {
		return fmt.Errorf("pureocr: write wxocr: %w", err)
	}

	// Extract ocr_model/.
	modelDir := filepath.Join(tmpDir, "ocr_model")
	if err := extractFS(ocrModelFS, ocrModelFSRoot, modelDir); err != nil {
		return fmt.Errorf("pureocr: extract ocr_model: %w", err)
	}

	// Extract ocr_helper binary (glibc, patchelf'd).
	helperPath := filepath.Join(tmpDir, "ocr_helper")
	if err := os.WriteFile(helperPath, ocrHelperData, 0755); err != nil {
		return fmt.Errorf("pureocr: write ocr_helper: %w", err)
	}

	hp, err := startHelper(helperPath, libPath, wxocrPath, tmpDir)
	if err != nil {
		return fmt.Errorf("pureocr: start ocr_helper: %w", err)
	}
	helper = hp
	return nil
}

// Stop shuts down the OCR helper and cleans up temp files.
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	if helper != nil {
		helper.close()
		helper = nil
	}
	if tmpDir != "" {
		os.RemoveAll(tmpDir)
		tmpDir = ""
	}
}

// OCRFile runs OCR on the image at the given path.
func OCRFile(imagePath string) (Result, error) {
	if err := ensureInit(); err != nil {
		return Result{}, err
	}

	mu.Lock()
	raw, err := helper.ocr(imagePath)
	mu.Unlock()

	if err != nil {
		return Result{}, err
	}

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

// ── helperProc: subprocess management ────────────────────────────────────────

type helperProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner
}

func startHelper(helperPath, libPath, wxocrPath, modelParent string) (*helperProc, error) {
	cmd := exec.Command(helperPath)
	// /opt/glibc must be on LD_LIBRARY_PATH so that libmmmojo.so's own
	// transitive dependencies (libglib-2.0, libatomic, libdl, …) are found
	// by the glibc dynamic linker inside the musl Alpine environment.
	ldPath := "/opt/glibc"
	if prev := os.Getenv("LD_LIBRARY_PATH"); prev != "" {
		ldPath = ldPath + ":" + prev
	}
	cmd.Env = append(os.Environ(),
		"OCR_LIB_PATH="+libPath,
		"OCR_WXOCR_PATH="+wxocrPath,
		"OCR_MODEL_DIR="+modelParent,
		"LD_LIBRARY_PATH="+ldPath,
	)
	cmd.Stderr = os.Stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &helperProc{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewScanner(stdout),
	}, nil
}

func (h *helperProc) ocr(imgPath string) (string, error) {
	req, _ := json.Marshal(map[string]string{"img": imgPath})
	if _, err := fmt.Fprintf(h.stdin, "%s\n", req); err != nil {
		return "", fmt.Errorf("pureocr: write to helper: %w", err)
	}
	if !h.stdout.Scan() {
		err := h.stdout.Err()
		if err == nil {
			err = fmt.Errorf("helper exited unexpectedly")
		}
		return "", fmt.Errorf("pureocr: read from helper: %w", err)
	}
	return h.stdout.Text(), nil
}

func (h *helperProc) close() {
	h.stdin.Close()
	h.cmd.Wait()
}

// ── extractFS ─────────────────────────────────────────────────────────────────

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
