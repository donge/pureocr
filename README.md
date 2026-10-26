# pureocr v2

On-device OCR for images (Chinese + English), on ONNX Runtime, with the models
**and** the runtime library embedded in the binary. No Python, no OpenCV, no
model download, no `/opt/ocr`.

```go
eng, err := pureocr.New(pureocr.Options{})
defer eng.Close()

res, err := eng.OCRFile("scan.png")
fmt.Println(res.Text())
```

## What changed in v2

v1 wrapped Tencent's WeChat OCR engine (`.so` + `.xnet` models, Chinese/English
screen text). v2 replaces the engine with [Indra OCR][indra]'s ONNX detector and
recognizers — a different engine, so this is a new major version:

| | v1 (WeChat OCR) | **v2 (Indra OCR)** |
|---|---|---|
| models | 10 closed-source `.xnet` files | 5 ONNX models, 16 MB |
| runtime | `libocr.so` + `wxocr` binary | ONNX Runtime (embedded, `dlopen`) |
| languages | Simplified Chinese, English | Chinese + English (document scans) |
| dense pages | dropped ~70% of a two-column page | full page |
| rotated / tilted pages | 0 lines / failed | quarter turns and ±45° tilt |
| reading order | engine order | XY-cut (two-column aware) |
| PDF support | none | none (images only) |
| API | `pureocr.OCRFile` | `pureocr.New` + `Engine`, same package helpers |

Measured on an M4 laptop (4 threads, 900×1200 scans): 128–250 ms per page,
0.23 s cold start, 330 MB single-page RSS; a two-column page that WeChat OCR
returned 27% of comes back complete.

## Install

```shell
go get github.com/donge/pureocr/v2
```

Requirements:

- **cgo** (`CGO_ENABLED=1`): the ONNX Runtime C API is called through cgo. The
  runtime library itself is loaded with `dlopen`, so there is no link-time
  dependency.
- Platforms with an embedded runtime: **linux/amd64**, **linux/arm64**,
  **darwin/arm64**. Elsewhere pass `Options.ORTLib` (or `PUREOCR_ORT_LIB`) with a
  path to `libonnxruntime.so`.
- Linux needs glibc ≥ 2.28 (the bundled runtime is a manylinux_2_28 build) and
  `libstdc++`. Alpine/musl: use a glibc base image.

Binary size: 49 MB (linux/arm64), 53 MB (linux/amd64) — models ~21 MB, ONNX
Runtime 24–29 MB, Go code 4 MB. The CJK recognizer is the fp32 model.

## API

```go
type Options struct {
	Threads     int    // ORT intra-op threads; 0 = min(NumCPU, 4)
	Concurrency int    // simultaneous OCR calls; 0 = NumCPU (calls wait, none fail)
	Batch       int    // recognizer batch; 0 = 1 (fastest on CPU)
	NoRotation  bool   // pages are known upright (skips orient.onnx)
	NoDeskew    bool   // pages are known level (skips the tilt search)
	ModelDir    string // load .onnx files from a directory instead of the binary
	ORTLib      string // path to libonnxruntime, instead of the embedded copy
}

type Charset int
const (
	CharsetMixed Charset = iota // Latin + CJK recognizers, routed per line (default)
	CharsetCJK                  // CJK only: one model, ~5 MB smaller, English pages slower
)

func New(Options) (*Engine, error)
func (e *Engine) Close() error
func (e *Engine) OCRFile(path string) (Result, error)     // PNG/JPEG/GIF/BMP/TIFF/WebP
func (e *Engine) OCRBytes(data []byte) (Result, error)
func (e *Engine) OCRImage(img image.Image) (Result, error)

type Result struct {
	Blocks   []Block       // text + confidence + box, in reading order
	Rotation int           // quarter turns undone
	Skew     float64       // degrees undone
	Elapsed  time.Duration
}
func (r Result) Text() string
```

`Result.Blocks[i].Rate` is the mean CTC peak probability; boxes are reported on
the page as it was given (rotation and deskew undone).

Package-level helpers use a process-wide default engine:

```go
res, err := pureocr.OCRBytes(data)
pureocr.Stop()   // release it
```

`CharsetCJK` loads only the CJK recognizer (which also reads Latin letters and
digits): the bundle loses `rec.onnx`/`script.onnx`, mixed Chinese/English lines
are read in one pass, but English-only pages get slower because the CJK model is
about twice as expensive per line. Use it when the corpus is Chinese-dominant.

### Raw pixels

`OCRGray` / `OCRRGB` take packed pixels (stride aware) and skip decoding
entirely — a PDF renderer or scanner driver that already has a buffer avoids a
PNG/JPEG round trip:

```go
res, err := eng.OCRRGB(rgb, width, height, stride)   // 3 bytes per pixel
```

The `Engine` is safe for concurrent use. `Concurrency` bounds parallel pages;
extra callers wait rather than fail. `Threads` is per *call*, so a single
`Engine` with `Concurrency: n` uses about `n × Threads` cores.

## CLI

```shell
go build ./example
./example scan.png                 # text
./example -json scan.png           # blocks with boxes and confidences
./example -threads 8 -no-rotate *.png
```

## Packaging

Every binary is self-contained: the ONNX models (16 MB) and the ONNX Runtime
library for the target platform are embedded, and the runtime library is
unpacked once to the user cache directory at first use.

```shell
make image             # bin/linux-amd64/{ocr,bench}, bin/linux-arm64/{ocr,bench}
make build             # CLI for the host platform
make test              # vet + tests (cgo)
```

`make image` builds inside the `golang:1.24-bookworm` image with
`CGO_ENABLED=1`, so each target is compiled natively (amd64 is emulated on
Apple silicon; ~5 min for both). Sizes: 53 MB (linux/amd64), 49 MB
(linux/arm64); stripping and `-trimpath` are on.

| platform | runtime bundled | notes |
|---|---|---|
| linux/amd64 | `lib/linux_amd64/libonnxruntime.so` | glibc ≥ 2.28, libstdc++ |
| linux/arm64 | `lib/linux_arm64/libonnxruntime.so` | glibc ≥ 2.28, libstdc++ |
| darwin/arm64 | `lib/darwin_arm64/libonnxruntime.dylib` | development on a Mac |

Another platform (or a smaller binary): set `Options.ORTLib` /
`PUREOCR_ORT_LIB` to a system ONNX Runtime and pass `-ldflags` to skip the
embedded copy — only the platform files under `lib/` that exist are embedded,
so a new target is one `//go:embed` line plus the library.

## Optional model overrides

A caller can load a different model folder with `Options.ModelDir`: the folder
needs `det.onnx`, `rec.onnx`, `rec_cjk.onnx`, `script.onnx`, `orient.onnx` and
`meta.json`. Model files are otherwise embedded in the binary.

## Performance

Same machine (M4, 4 CPU), same ONNX Runtime version, per page:

| page | Indra Python (default) | Indra Python (tuned) | **pureocr v2** |
|---|---|---|---|
| Chinese paragraph, 1500 px | 86 ms | 55 ms | **76 ms** |
| Chinese essay, 900×1200 | 202 ms | 151 ms | **158 ms** |
| Two-column paper, 900×1200 | 339 ms | 259 ms | **247 ms** |
| Table, 900×1168 | 272 ms | 293 ms | **244 ms** |

Batch (40 mixed zh/en pages, 4-way): 3.9 s tuned Python, 4.0 s v2 — the models
are the bottleneck, not the language. v2 wins where Python cannot: 0.23 s cold
start (vs 1.66 s), 0.9 GB for four concurrent pages (vs 3.1 GB in four
processes), and a single self-contained binary.

## Accuracy

- Chinese and English, printed and handwritten, dense pages, multi-column
  reading order, pages turned by 90/180/270° or tilted up to 45°.
- On clean text the character error rate matches the Python reference
  (0.0% English, 2.6% Chinese on the synthetic set).
- Not supported: PDF input, the n-gram language model (Indra's `lm*.npz`), the
  layout model (`order.onnx`: block labels and markdown), and scripts other than
  Latin/CJK (Korean, Thai, Arabic read as garbage).

## Layout

```
pureocr.go     public API, default engine
engine.go      pipeline: straighten, turn, detect, read, order
detect.go      detector input + connected-component line decoding
recognize.go   line crops, script routing, CTC decoding
order.go       XY-cut reading order
orient.go      quarter-turn classifier, tilt search
model.go       ONNX sessions, vocabularies
ortlib.go      ONNX Runtime loading (embedded → cache dir → dlopen)
image.go       grayscale, resampling, rotation
models/        ONNX weights + their licence
lib/<os>_<arch>/  ONNX Runtime shared library per platform
```

## Licences

The Go code is independent of the model licence. The ONNX weights in `models/`
are Indra OCR's, under the AI PUBS Open RAIL-M licence (modified); see
[`models/NOTICE.md`](models/NOTICE.md).

[indra]: https://github.com/datalab-to/indra
