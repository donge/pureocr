//go:build linux && amd64

package pureocr

import "embed"

//go:embed assets/amd64/libocr.so assets/amd64/libmmmojo.so assets/amd64/wxocr assets/ocr_model
var ocrFS embed.FS

const archPrefix = "assets/amd64"
