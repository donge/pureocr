// Command ocr prints the text of an image.
//
//	ocr scan.png
//	ocr -json scan.png
//
// Build: cgo is required (ONNX Runtime is called through cgo).
//
//	go build ./example
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/donge/pureocr/v2"
)

func main() {
	asJSON := flag.Bool("json", false, "print blocks as JSON")
	threads := flag.Int("threads", 0, "ONNX Runtime threads (default: min(NumCPU,4))")
	noRotate := flag.Bool("no-rotate", false, "assume pages are upright")
	noDeskew := flag.Bool("no-deskew", false, "assume pages are level")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ocr [-json] [-threads N] image...")
		os.Exit(2)
	}

	eng, err := pureocr.New(pureocr.Options{
		Threads:     *threads,
		NoRotation:  *noRotate,
		NoDeskew:    *noDeskew,
		Concurrency: runtime.NumCPU(),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: %v\n", err)
		os.Exit(1)
	}
	defer eng.Close()

	for _, path := range args {
		res, err := eng.OCRFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", path, err)
			os.Exit(1)
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", " ")
			enc.Encode(res)
			continue
		}
		fmt.Fprintf(os.Stderr, "%s: %d lines in %v\n", path, len(res.Blocks), res.Elapsed)
		fmt.Println(res.Text())
	}
}
