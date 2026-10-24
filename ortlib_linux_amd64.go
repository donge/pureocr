//go:build linux && amd64

package pureocr

import _ "embed"

//go:embed lib/linux_amd64/libonnxruntime.so
var ortLibData []byte

const ortLibName = "libonnxruntime.so"

func embeddedORTLib() ([]byte, string) { return ortLibData, ortLibName }
