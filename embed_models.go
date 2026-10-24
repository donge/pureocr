package pureocr

import "embed"

// The ONNX models are part of the binary: det (line detection), rec (Latin),
// rec_cjk (Chinese), script (line classifier) and orient (quarter turns), plus
// the vocabulary and geometry in meta.json. About 16 MB in total.
//
//go:embed models/det.onnx models/rec.onnx models/rec_cjk.onnx models/script.onnx models/orient.onnx models/meta.json
var modelFS embed.FS

func embeddedModel(name string) ([]byte, error) {
	return modelFS.ReadFile("models/" + name)
}
