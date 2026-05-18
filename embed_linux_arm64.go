//go:build linux && arm64

package pureocr

import (
	"embed"
)

//go:embed embed/linux_arm64/libpureocr.so
var libpureocrData []byte

//go:embed embed/linux_arm64/wxocr
var wxocrData []byte

//go:embed embed/linux_arm64/ocr_model
var ocrModelFS embed.FS

const ocrModelFSRoot = "embed/linux_arm64/ocr_model"

//go:embed embed/linux_arm64/ocr_helper
var ocrHelperData []byte
