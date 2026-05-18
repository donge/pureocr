//go:build linux && arm64

// ocr_helper is a standalone glibc binary that drives the WeChat OCR engine.
// It is launched by the pureocr package (musl tequila) as a subprocess.
//
// Required environment variables (set by the parent pureocr process):
//
//	OCR_LIB_PATH   — absolute path to libmmmojo.so
//	OCR_WXOCR_PATH — absolute path to the wxocr executable
//	OCR_MODEL_DIR  — absolute path to the directory containing ocr_model/
//
// Communication: newline-delimited JSON on stdin/stdout.
//
//	request:  {"img":"/absolute/path/to/image.png"}
//	response: {"errcode":0,"ocr_response":[{"text":"...","rate":0.99,...},...]}
//	error:    {"errcode":-1,"error":"..."}
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/donge/pureocr/internal/engine"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ocr_helper: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	libPath := os.Getenv("OCR_LIB_PATH")
	wxocrPath := os.Getenv("OCR_WXOCR_PATH")
	modelDir := os.Getenv("OCR_MODEL_DIR")

	if libPath == "" || wxocrPath == "" || modelDir == "" {
		return fmt.Errorf("OCR_LIB_PATH, OCR_WXOCR_PATH and OCR_MODEL_DIR must be set")
	}

	// LD_LIBRARY_PATH must include the directory containing libmmmojo.so so that
	// the wxocr child process can resolve it at runtime.
	_ = os.Setenv("LD_LIBRARY_PATH", filepath.Dir(libPath)+":"+os.Getenv("LD_LIBRARY_PATH"))

	mojo, err := engine.LoadMojo(libPath)
	if err != nil {
		return fmt.Errorf("load mojo: %w", err)
	}

	// modelDir is the parent of ocr_model/; engine uses it as exeDir.
	eng, err := engine.New(mojo, wxocrPath, modelDir)
	if err != nil {
		return fmt.Errorf("start engine: %w", err)
	}
	defer eng.Stop()

	type request struct {
		Img string `json:"img"`
	}
	enc := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			enc.Encode(map[string]interface{}{"errcode": -1, "error": "bad request: " + err.Error()})
			continue
		}
		result, err := eng.OCRFile(req.Img)
		if err != nil {
			enc.Encode(map[string]interface{}{"errcode": -1, "error": err.Error()})
			continue
		}
		enc.Encode(result)
	}
	return sc.Err()
}
