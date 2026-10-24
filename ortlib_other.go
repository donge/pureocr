//go:build !(linux && amd64) && !(linux && arm64) && !(darwin && arm64)

package pureocr

// No ONNX Runtime is bundled for this platform: pass Options.ORTLib or set
// PUREOCR_ORT_LIB.
func embeddedORTLib() ([]byte, string) { return nil, "" }
