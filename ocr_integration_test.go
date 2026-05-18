//go:build linux && (amd64 || arm64) && integration

package pureocr_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/donge/pureocr"
)

func TestOCRFile(t *testing.T) {
	imgPath := os.Getenv("TEST_IMG")
	if imgPath == "" {
		t.Skip("TEST_IMG not set")
	}

	result, err := pureocr.OCRFile(imgPath)
	if err != nil {
		t.Fatalf("OCRFile: %v", err)
	}
	if len(result.Blocks) == 0 {
		t.Fatalf("no OCR blocks returned (errcode=%d)", result.ErrCode)
	}
	fmt.Printf("OCR result (%d blocks):\n%s\n", len(result.Blocks), result.Text())
}
