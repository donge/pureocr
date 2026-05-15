//go:build linux && amd64

package pureocr

import (
	"embed"
	_ "embed"
)

//go:embed embed/linux_amd64/libocr.so
var libocrData []byte

//go:embed embed/linux_amd64/libpureocr.so
var libpureocrData []byte

//go:embed embed/linux_amd64/wxocr
var wxocrData []byte

//go:embed embed/ocr_model
var ocrModelFS embed.FS
