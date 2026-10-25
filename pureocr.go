// Package pureocr is an on-device OCR engine for images (Chinese + English),
// built on ONNX Runtime with the models embedded in the binary.
//
// Indra OCR's detection and recognition models run through ONNX Runtime; the
// pre- and post-processing (page normalization, connected-component line
// decoding, line crops, script routing, CTC decoding, reading order) is Go.
// Nothing is installed on the host: the ONNX models and the ONNX Runtime
// library for the target platform are embedded and unpacked at startup.
//
//	v2 API:
//	  eng, err := pureocr.New(pureocr.Options{})
//	  defer eng.Close()
//	  res, err := eng.OCRFile("scan.png")
//	  fmt.Println(res.Text())
//
// A package-level default engine backs OCRFile/OCRBytes:
//
//	res, err := pureocr.OCRBytes(png)
//
// Supported platforms: linux/amd64, linux/arm64, darwin/arm64. cgo is required
// (the ONNX Runtime C API is called through cgo); the runtime library itself is
// loaded with dlopen at startup.
package pureocr

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Block is one recognised text line.
type Block struct {
	Text   string  `json:"text"`
	Rate   float64 `json:"rate"` // mean CTC peak probability, 0..1
	Left   float64 `json:"left"`
	Top    float64 `json:"top"`
	Right  float64 `json:"right"`
	Bottom float64 `json:"bottom"`
}

// Result is the OCR result for one page.
type Result struct {
	Blocks []Block `json:"ocr_response"`
	// Rotation is the number of quarter turns (counter-clockwise) undone before
	// recognition; Skew is the tilt in degrees undone. Boxes are reported on
	// the page as it was given.
	Rotation int     `json:"rotation"`
	Skew     float64 `json:"skew"`
	// Elapsed is the wall time of the recognition (models already loaded).
	Elapsed time.Duration `json:"elapsed_ns"`
}

// Text returns the recognised lines joined by newlines, in reading order.
func (r Result) Text() string {
	parts := make([]string, 0, len(r.Blocks))
	for _, b := range r.Blocks {
		if b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// Options configures an Engine. The zero value is usable: models and the ONNX
// Runtime library come from the binary.
type Options struct {
	// Threads is the ONNX Runtime intra-op thread count per recognition. 0 uses
	// min(NumCPU, 4): more threads stop helping on these small models.
	Threads int
	// Concurrency caps simultaneous OCR calls on one Engine. 0 uses NumCPU.
	// Calls over the limit wait; they are never rejected.
	Concurrency int
	// Batch groups lines of similar width into one recognizer call. 1 (the
	// default) is fastest on CPU; larger values only pay off on very wide pages.
	Batch int
	// NoRotation disables the orientation model (pages are known upright).
	NoRotation bool
	// NoDeskew disables the tilt search (pages are known level).
	NoDeskew bool
	// ModelDir reads det.onnx/rec.onnx/rec_cjk.onnx/script.onnx/orient.onnx/
	// meta.json from a directory instead of the embedded copies.
	ModelDir string
	// ORTLib is the path of the ONNX Runtime shared library to load. Empty uses
	// the embedded library (or PUREOCR_ORT_LIB).
	ORTLib string
}

// ErrClosed is returned by an Engine after Close.
var ErrClosed = errors.New("pureocr: engine closed")

// ErrUnsupportedPlatform means the binary has no ONNX Runtime for this GOOS/GOARCH.
var ErrUnsupportedPlatform = errors.New("pureocr: no ONNX Runtime bundled for this platform")

// Engine is an OCR engine. It is safe for concurrent use; use Options.Concurrency
// to bound the number of simultaneous recognitions.
type Engine struct {
	models   *models
	sem      chan struct{}
	batch    int
	rotate   bool
	deskew   bool
	closed   bool
	closeMux sync.RWMutex
}

// New loads the models and returns an Engine. The first call in a process also
// loads the ONNX Runtime library (about 20 ms).
func New(opts Options) (*Engine, error) {
	if opts.Threads <= 0 {
		opts.Threads = min(runtime.NumCPU(), 4)
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = max(runtime.NumCPU(), 1)
	}
	if opts.Batch <= 0 {
		opts.Batch = 1
	}
	if err := loadORT(opts.ORTLib); err != nil {
		return nil, err
	}
	m, err := openModels(opts.ModelDir, opts.Threads)
	if err != nil {
		return nil, err
	}
	return &Engine{
		models: m,
		sem:    make(chan struct{}, opts.Concurrency),
		batch:  opts.Batch,
		rotate: !opts.NoRotation,
		deskew: !opts.NoDeskew,
	}, nil
}

// Close releases the sessions. The ONNX Runtime library stays loaded for the
// process (other engines may still use it).
func (e *Engine) Close() error {
	e.closeMux.Lock()
	defer e.closeMux.Unlock()
	if e.closed {
		return nil
	}
	e.closed = true
	return e.models.close()
}

func (e *Engine) acquire() error {
	e.closeMux.RLock()
	closed := e.closed
	e.closeMux.RUnlock()
	if closed {
		return ErrClosed
	}
	e.sem <- struct{}{}
	return nil
}

func (e *Engine) release() { <-e.sem }

// OCRFile recognises the image file at path (PNG, JPEG, GIF, BMP, TIFF, WebP;
// multi-frame images use the first frame).
func (e *Engine) OCRFile(path string) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return Result{}, fmt.Errorf("pureocr: decode %s: %w", path, err)
	}
	return e.OCRImage(img)
}

// OCRBytes recognises an encoded image (PNG, JPEG, GIF, BMP, TIFF, WebP).
func (e *Engine) OCRBytes(data []byte) (Result, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Result{}, fmt.Errorf("pureocr: decode image: %w", err)
	}
	return e.OCRImage(img)
}

// OCRImage recognises an image. The page is turned upright (orientation model)
// and straightened (tilt search) before detection.
func (e *Engine) OCRImage(img image.Image) (Result, error) {
	if img == nil {
		return Result{}, errors.New("pureocr: nil image")
	}
	return e.runGray(toGray(img))
}

// OCRGray recognises a packed 8-bit grayscale image (row stride in bytes may be
// larger than the width). It skips decoding entirely: a caller that already has
// pixels (a PDF renderer, a scanner driver) avoids a PNG/JPEG round trip.
func (e *Engine) OCRGray(pix []byte, w, h, stride int) (Result, error) {
	if w <= 0 || h <= 0 || stride < w || len(pix) < stride*(h-1)+w {
		return Result{}, errInvalidPixels
	}
	g := &grayImg{pix: make([]uint8, w*h), w: w, h: h}
	for y := 0; y < h; y++ {
		copy(g.pix[y*w:(y+1)*w], pix[y*stride:y*stride+w])
	}
	return e.runGray(g)
}

// OCRRGB recognises a packed RGB24 image (3 bytes per pixel, row stride in bytes),
// converting to gray in one pass.
func (e *Engine) OCRRGB(pix []byte, w, h, stride int) (Result, error) {
	if w <= 0 || h <= 0 || stride < 3*w || len(pix) < stride*(h-1)+3*w {
		return Result{}, errInvalidPixels
	}
	g := &grayImg{pix: make([]uint8, w*h), w: w, h: h}
	for y := 0; y < h; y++ {
		row := g.pix[y*w : (y+1)*w]
		src := pix[y*stride : y*stride+3*w]
		for x := range row {
			i := 3 * x
			row[x] = uint8((19595*uint32(src[i]) + 38470*uint32(src[i+1]) + 7471*uint32(src[i+2]) + 32768) >> 16)
		}
	}
	return e.runGray(g)
}

// runGray runs the pipeline on an already-decoded page.
func (e *Engine) runGray(g *grayImg) (Result, error) {
	if err := e.acquire(); err != nil {
		return Result{}, err
	}
	defer e.release()
	start := time.Now()
	res, err := e.process(g)
	if err != nil {
		return Result{}, err
	}
	res.Elapsed = time.Since(start)
	return res, nil
}

var errInvalidPixels = errors.New("pureocr: pixel buffer smaller than width*height")

// ---------------------------------------------------------------- default engine

var (
	defOnce sync.Mutex
	defEng  *Engine
	defErr  error
)

// Default returns the process-wide engine used by OCRFile and OCRBytes.
func Default() (*Engine, error) {
	defOnce.Lock()
	defer defOnce.Unlock()
	if defEng == nil && defErr == nil {
		defEng, defErr = New(Options{})
	}
	return defEng, defErr
}

// OCRFile recognises an image file with the default engine.
func OCRFile(path string) (Result, error) {
	eng, err := Default()
	if err != nil {
		return Result{}, err
	}
	return eng.OCRFile(path)
}

// OCRBytes recognises an encoded image with the default engine.
func OCRBytes(data []byte) (Result, error) {
	eng, err := Default()
	if err != nil {
		return Result{}, err
	}
	return eng.OCRBytes(data)
}

// Stop closes the default engine and releases its sessions. A later call
// re-creates it. It does not unload the ONNX Runtime library.
func Stop() {
	defOnce.Lock()
	defer defOnce.Unlock()
	if defEng != nil {
		defEng.Close()
		defEng = nil
	}
	defErr = nil
}

// ---------------------------------------------------------------- helpers
