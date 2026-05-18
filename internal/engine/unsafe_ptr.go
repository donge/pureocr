//go:build linux && arm64

package engine

import "unsafe"

// ptrToSlice converts a raw C pointer (uintptr) to a []byte of length n.
//
//go:nocheckptr
func ptrToSlice(ptr uintptr, n int) []byte {
	p := (*byte)(unsafe.Pointer(ptr)) // nolint: unsafeptr
	return unsafe.Slice(p, n)
}
