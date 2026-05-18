//go:build linux && arm64

package pureocr

// Mojo IPC bindings for libpureocr.so (= libmmmojo.so).
//
// Canonical C header: mmmojo_funcs.h (from swigger/wechat-ocr)
//
// SetMMMojoEnvironmentCallbacks slot enum (mmmojo.h MMMojoEnvironmentCallbackType):
//   0 = kMMUserData                 → variadic: void* user_data (this pointer)
//   1 = kMMReadPush                 → func(uint32 request_id, *void readinfo, *void userdata)
//   2 = kMMReadPull
//   3 = kMMReadShared
//   4 = kMMRemoteConnect            → func(bool is_connected, *void userdata)
//   5 = kMMRemoteDisconnect         → func(*void userdata)
//   6 = kMMRemoteProcessLaunched    → func(*void userdata)
//   7 = kMMRemoteProcessLaunchFailed→ func(int error_code, *void userdata)
//   8 = kMMRemoteMojoError          → func(*void errorbuf, int errorsize, *void userdata)
//
// SetMMMojoEnvironmentInitParams slot enum (mmmojo.h MMMojoEnvironmentInitParamType):
//   0 = kMMHostProcess              → variadic: bool true
//   1 = kMMLoopStartThread
//   2 = kMMExePath                  → variadic: const char* path
//   3 = kMMLogPath (unused)
//   4 = kMMLogToStderr (unused)
//
// GetMMMojoReadInfoRequest(readinfo, *uint32 out_size) → *void data
// GetMMMojoWriteInfoRequest(writeinfo, size_t data_size) → *void buf
// CreateMMMojoWriteInfo(int method, bool sync, uint32 request_id) → *void writeinfo
//   method: kMMPush=1, kMMPullReq=2, kMMPullResp=3, kMMShared=4
//
// wx4 request IDs (RequestIdOCR4):
//   HAND_SHAKE = 10001
//   REQ_OCR    = 10010
//   RESP_OCR   = 10011

import (
	"fmt"

	"github.com/ebitengine/purego"
)

const (
	// MMMojoEnvironmentCallbackType
	kMMUserData                  = 0
	kMMReadPush                  = 1
	kMMReadPull                  = 2
	kMMReadShared                = 3
	kMMRemoteConnect             = 4
	kMMRemoteDisconnect          = 5
	kMMRemoteProcessLaunched     = 6
	kMMRemoteProcessLaunchFailed = 7
	kMMRemoteMojoError           = 8

	// MMMojoEnvironmentInitParamType
	kMMHostProcess    = 0
	kMMExePath        = 2
	kMMLogPath        = 3
	kMMLogToStderr    = 4

	// MMMojoInfoMethod
	kMMPush = 1

	// wx4 RequestIdOCR4
	reqHandShake = uint32(10001)
	reqOCR       = uint32(10010)
	respOCR      = uint32(10011)
)

// mojoLib holds the loaded libpureocr.so handle and resolved function pointers.
type mojoLib struct {
	lib uintptr

	InitializeMMMojo               func(argc int32, argv uintptr)
	CreateMMMojoEnvironment        func() uintptr
	RemoveMMMojoEnvironment        func(env uintptr)
	SetMMMojoEnvironmentCallbacks  func(env uintptr, slot int32, val uintptr)
	SetMMMojoEnvironmentInitParams func(env uintptr, slot int32, val uintptr)
	AppendMMSubProcessSwitchNative func(env uintptr, key uintptr, val uintptr)
	StartMMMojoEnvironment         func(env uintptr)
	StopMMMojoEnvironment          func(env uintptr)

	GetMMMojoReadInfoRequest func(readInfo uintptr, outSize uintptr) uintptr
	RemoveMMMojoReadInfo     func(readInfo uintptr)

	CreateMMMojoWriteInfo    func(method int32, sync bool, requestID uint32) uintptr
	GetMMMojoWriteInfoRequest func(writeInfo uintptr, dataSize uint64) uintptr
	SendMMMojoWriteInfo      func(env uintptr, writeInfo uintptr) bool
	RemoveMMMojoWriteInfo    func(writeInfo uintptr)
}

// loadMojo opens libpureocr.so and resolves all symbols.
func loadMojo(soPath string) (*mojoLib, error) {
	lib, err := purego.Dlopen(soPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("dlopen libpureocr.so: %w", err)
	}
	m := &mojoLib{lib: lib}

	purego.RegisterLibFunc(&m.InitializeMMMojo, lib, "InitializeMMMojo")
	purego.RegisterLibFunc(&m.CreateMMMojoEnvironment, lib, "CreateMMMojoEnvironment")
	purego.RegisterLibFunc(&m.RemoveMMMojoEnvironment, lib, "RemoveMMMojoEnvironment")
	purego.RegisterLibFunc(&m.SetMMMojoEnvironmentCallbacks, lib, "SetMMMojoEnvironmentCallbacks")
	purego.RegisterLibFunc(&m.SetMMMojoEnvironmentInitParams, lib, "SetMMMojoEnvironmentInitParams")
	purego.RegisterLibFunc(&m.AppendMMSubProcessSwitchNative, lib, "AppendMMSubProcessSwitchNative")
	purego.RegisterLibFunc(&m.StartMMMojoEnvironment, lib, "StartMMMojoEnvironment")
	purego.RegisterLibFunc(&m.StopMMMojoEnvironment, lib, "StopMMMojoEnvironment")
	purego.RegisterLibFunc(&m.GetMMMojoReadInfoRequest, lib, "GetMMMojoReadInfoRequest")
	purego.RegisterLibFunc(&m.RemoveMMMojoReadInfo, lib, "RemoveMMMojoReadInfo")
	purego.RegisterLibFunc(&m.CreateMMMojoWriteInfo, lib, "CreateMMMojoWriteInfo")
	purego.RegisterLibFunc(&m.GetMMMojoWriteInfoRequest, lib, "GetMMMojoWriteInfoRequest")
	purego.RegisterLibFunc(&m.SendMMMojoWriteInfo, lib, "SendMMMojoWriteInfo")
	purego.RegisterLibFunc(&m.RemoveMMMojoWriteInfo, lib, "RemoveMMMojoWriteInfo")

	m.InitializeMMMojo(0, 0)
	return m, nil
}
