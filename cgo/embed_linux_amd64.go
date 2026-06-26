//go:build linux && amd64

package cgo

import "embed"

//go:embed assets/amd64/libmmmojo.so assets/amd64/wxocr assets/ocr_model
var cgoFS embed.FS

const archPrefix = "assets/amd64"
