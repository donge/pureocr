//go:build linux && arm64

package engine

// Mojo IPC bindings for libmmmojo.so (= libpureocr.so).
//
// SetMMMojoEnvironmentCallbacks slot enum:
//   0 = kMMUserData
//   1 = kMMReadPush    → func(uint32 request_id, uintptr readinfo, uintptr userdata)
//   2 = kMMReadPull
//   3 = kMMReadShared
//   4 = kMMRemoteConnect            → func(bool is_connected, uintptr userdata)
//   5 = kMMRemoteDisconnect         → func(uintptr userdata)
//   6 = kMMRemoteProcessLaunched    → func(uintptr userdata)
//   7 = kMMRemoteProcessLaunchFailed→ func(int32 error_code, uintptr userdata)
//   8 = kMMRemoteMojoError          → func(uintptr errorbuf, int32 errorsize, uintptr userdata)
//
// SetMMMojoEnvironmentInitParams slot enum:
//   0 = kMMHostProcess  → bool true
//   2 = kMMExePath      → const char* path
//
// wx4 RequestIdOCR4: HAND_SHAKE=10001, REQ_OCR=10010, RESP_OCR=10011

import (
	"fmt"

	"github.com/ebitengine/purego"
)

const (
	kMMReadPush                  = 1
	kMMReadPull                  = 2
	kMMReadShared                = 3
	kMMRemoteConnect             = 4
	kMMRemoteDisconnect          = 5
	kMMRemoteProcessLaunched     = 6
	kMMRemoteProcessLaunchFailed = 7
	kMMRemoteMojoError           = 8

	kMMHostProcess = 0
	kMMExePath     = 2

	kMMPush = 1

	ReqHandShake = uint32(10001)
	ReqOCR       = uint32(10010)
	RespOCR      = uint32(10011)
)

// MojoLib holds the loaded libmmmojo.so handle and resolved function pointers.
type MojoLib struct {
	lib uintptr

	InitializeMMMojo               func(argc int32, argv uintptr)
	CreateMMMojoEnvironment        func() uintptr
	RemoveMMMojoEnvironment        func(env uintptr)
	SetMMMojoEnvironmentCallbacks  func(env uintptr, slot int32, val uintptr)
	SetMMMojoEnvironmentInitParams func(env uintptr, slot int32, val uintptr)
	AppendMMSubProcessSwitchNative func(env uintptr, key uintptr, val uintptr)
	StartMMMojoEnvironment         func(env uintptr)
	StopMMMojoEnvironment          func(env uintptr)

	GetMMMojoReadInfoRequest  func(readInfo uintptr, outSize uintptr) uintptr
	RemoveMMMojoReadInfo      func(readInfo uintptr)
	CreateMMMojoWriteInfo     func(method int32, sync bool, requestID uint32) uintptr
	GetMMMojoWriteInfoRequest func(writeInfo uintptr, dataSize uint64) uintptr
	SendMMMojoWriteInfo       func(env uintptr, writeInfo uintptr) bool
	RemoveMMMojoWriteInfo     func(writeInfo uintptr)
}

// LoadMojo opens libmmmojo.so and resolves all symbols.
func LoadMojo(soPath string) (*MojoLib, error) {
	lib, err := purego.Dlopen(soPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("dlopen %s: %w", soPath, err)
	}
	m := &MojoLib{lib: lib}

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
