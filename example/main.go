//go:build linux && (amd64 || arm64)

package main

import (
	"fmt"
	"os"

	pureocr "github.com/donge/pureocr"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: example <image-file>")
		os.Exit(1)
	}

	result, err := pureocr.OCRFile(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(result.Text())
}
