//go:build linux && arm64

package pureocr

import "embed"

//go:embed assets/arm64/libocr.so assets/arm64/libmmmojo.so assets/arm64/wxocr assets/ocr_model
var ocrFS embed.FS

const archPrefix = "assets/arm64"
const modelPrefix = "assets/ocr_model"
