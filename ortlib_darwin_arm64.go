//go:build darwin && arm64

package pureocr

import _ "embed"

//go:embed lib/darwin_arm64/libonnxruntime.dylib
var ortLibData []byte

const ortLibName = "libonnxruntime.dylib"

func embeddedORTLib() ([]byte, string) { return ortLibData, ortLibName }
