//go:build linux && arm64

package cgo

import "embed"

//go:embed assets/arm64/libmmmojo.so assets/arm64/wxocr assets/ocr_model
var cgoFS embed.FS

const archPrefix = "assets/arm64"
