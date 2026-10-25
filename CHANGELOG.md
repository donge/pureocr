# Changelog

## v2.1.0

### Added

- `Options.Charset`: `CharsetMixed` (default) loads the Latin and CJK
  recognizers; `CharsetCJK` loads only the CJK one (≈5 MB smaller, reads mixed
  lines in one pass, English-only pages slower).
- `Engine.OCRGray` / `Engine.OCRRGB`: recognise packed pixels with no decoding,
  for callers that already have a buffer (a PDF renderer, a scanner).
- `Engine.DumpLineCrops` and `tools/quantize_rec.py`: dump the recognizer's
  inputs and rebuild the int8 model on your own domain.
- `make image`: linux/amd64 + linux/arm64 binaries; CI runs tests on Linux and
  macOS and packages both architectures.

### Changed

- `models/rec_cjk.onnx` is int8 (3.0 MB instead of 10.2 MB) and 1.6x faster per
  line; 96.5% of the fp32 text is unchanged. The LM outputs are removed.
- One downscaled preview is shared by the tilt search, the orientation model and
  the detector instead of three full-page resizes.

## v2.0.0

The engine is replaced: v2 runs Indra OCR's ONNX models on ONNX Runtime instead
of wrapping Tencent's WeChat OCR engine. This is a breaking release.

### Changed

- **Engine**: Indra OCR detector + Latin/CJK recognizers (`det.onnx`,
  `rec.onnx`, `rec_cjk.onnx`, `script.onnx`, `orient.onnx`), embedded in the
  binary.
- **API**: `pureocr.New(pureocr.Options{})` returns an `*Engine`
  (`OCRFile`/`OCRBytes`/`OCRImage`/`Close`). The package-level `OCRFile`,
  `OCRBytes` and `Stop` still work on a default engine.
- **Concurrency**: an `Engine` is safe for concurrent use; `Options.Concurrency`
  bounds parallel pages and callers wait instead of failing. `ErrOCRBusy` is
  gone.
- **Platforms**: linux/amd64, linux/arm64, darwin/arm64. The ONNX Runtime shared
  library for the target platform is embedded and unpacked to the user cache
  directory at first use (`Options.ORTLib` / `PUREOCR_ORT_LIB` override it).
  cgo is now required.
- **Quality**: dense and multi-column pages keep their text (WeChat OCR dropped
  most of it), pages turned by 90/180/270° or tilted up to 45° are handled, and
  lines come out in reading order.
- **Result**: `Block` keeps `Text`, `Rate` and the box; `Result` adds
  `Rotation`, `Skew` and `Elapsed`. `ErrCode` is gone.

### Removed

- `assets/` (Tencent `.xnet` models and `.so` files), the `pureocr/cgo`
  subpackage, and the `purego` dependency.

### Requirements

- Go 1.24+, cgo, glibc 2.28+ on Linux.
- Binary size is about 45 MB (models 16 MB, ONNX Runtime 24–32 MB).

## v0.4.0 and earlier

v1 wrapped WeChat OCR: embedded `libocr.so`, `libmmmojo.so` and `wxocr`, a
process-wide serialized engine with bounded non-blocking admission control
(`ErrOCRBusy`), and a `cgo` subpackage with statically linked libraries.
