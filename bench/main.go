// Command bench measures end-to-end OCR time per image: one warm-up pass, then
// n timed passes (file decode included), and prints JSON per image.
//
//	bench -n 5 -threads 4 -jobs 4 -json out.json images/*.png
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/donge/pureocr/v2"
)

type result struct {
	Image  string   `json:"image"`
	Runs   int      `json:"runs"`
	MinMs  float64  `json:"min_ms"`
	MedMs  float64  `json:"med_ms"`
	MaxMs  float64  `json:"max_ms"`
	Blocks int      `json:"blocks"`
	Chars  int      `json:"chars"`
	Lines  []string `json:"lines"`
	Err    string   `json:"err,omitempty"`
}

func benchOne(eng *pureocr.Engine, img string, runs int) result {
	r := result{Image: img, Runs: runs}
	if _, err := eng.OCRFile(img); err != nil { // warm-up
		r.Err = err.Error()
		return r
	}
	times := make([]float64, 0, runs)
	var last pureocr.Result
	for k := 0; k < runs; k++ {
		start := time.Now()
		res, err := eng.OCRFile(img)
		if err != nil {
			r.Err = err.Error()
			return r
		}
		times = append(times, float64(time.Since(start).Microseconds())/1000)
		last = res
	}
	sort.Float64s(times)
	r.MinMs = times[0]
	r.MedMs = times[len(times)/2]
	r.MaxMs = times[len(times)-1]
	r.Blocks = len(last.Blocks)
	r.Chars = len([]rune(last.Text()))
	for _, b := range last.Blocks {
		r.Lines = append(r.Lines, b.Text)
	}
	fmt.Fprintf(os.Stderr, "%-40s med=%7.1fms min=%7.1fms blocks=%3d chars=%d\n",
		filepath.Base(img), r.MedMs, r.MinMs, r.Blocks, r.Chars)
	return r
}

func main() {
	runs := flag.Int("n", 5, "timed runs per image")
	threads := flag.Int("threads", 4, "ONNX Runtime threads per recognition")
	workers := flag.Int("jobs", 1, "concurrent images")
	batch := flag.Int("batch", 1, "recognizer batch (lines per run)")
	noRotate := flag.Bool("no-rotate", false, "assume pages are upright")
	noDeskew := flag.Bool("no-deskew", false, "assume pages are level")
	models := flag.String("models", "", "model folder (default: embedded)")
	dumpCrops := flag.String("dump-crops", "", "write detected line crops (PNG) here")
	cjk := flag.Bool("cjk", false, "load only the CJK recognizer (CharsetCJK)")
	jsonOut := flag.String("json", "", "write results here")
	flag.Parse()
	imgs := flag.Args()
	if len(imgs) == 0 {
		fmt.Fprintln(os.Stderr, "usage: bench [-n 5] [-threads 4] [-jobs 1] images...")
		os.Exit(2)
	}

	charset := pureocr.CharsetMixed
	if *cjk {
		charset = pureocr.CharsetCJK
	}
	eng, err := pureocr.New(pureocr.Options{
		Threads: *threads, Concurrency: *workers, Batch: *batch, Charset: charset,
		NoRotation: *noRotate, NoDeskew: *noDeskew, ModelDir: *models,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "init: %v\n", err)
		os.Exit(1)
	}
	defer eng.Close()

	if *dumpCrops != "" {
		n, err := eng.DumpLineCrops(imgs[0], *dumpCrops)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dump crops: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "wrote %d crops to %s\n", n, *dumpCrops)
		return
	}
	results := make([]result, len(imgs))
	sem := make(chan struct{}, *workers)
	var wg sync.WaitGroup
	for i, img := range imgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, img string) {
			defer wg.Done()
			defer func() { <-sem }()
			results[i] = benchOne(eng, img, *runs)
		}(i, img)
	}
	wg.Wait()

	blob, _ := json.MarshalIndent(results, "", " ")
	if *jsonOut != "" {
		os.WriteFile(*jsonOut, blob, 0o644)
	}
	os.Stdout.Write(blob)
	os.Stdout.Write([]byte("\n"))
}
