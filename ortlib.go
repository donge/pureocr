package pureocr

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// ONNX Runtime is loaded once per process: dlopen the shared library, then
// initialize the environment.
var (
	ortOnce sync.Once
	ortErr  error
)

func loadORT(explicit string) error {
	ortOnce.Do(func() {
		path, err := resolveORTLib(explicit)
		if err != nil {
			ortErr = err
			return
		}
		ort.SetSharedLibraryPath(path)
		ortErr = ort.InitializeEnvironment()
		if ortErr != nil {
			ortErr = fmt.Errorf("pureocr: initialize ONNX Runtime (%s): %w", path, ortErr)
		}
	})
	if ortErr != nil && errors.Is(ortErr, ErrUnsupportedPlatform) {
		return ortErr
	}
	return ortErr
}

// resolveORTLib picks the ONNX Runtime library: an explicit path, the
// PUREOCR_ORT_LIB environment variable, the embedded copy (unpacked to the user
// cache directory), or the system loader path.
func resolveORTLib(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("PUREOCR_ORT_LIB"); env != "" {
		return env, nil
	}
	data, name := embeddedORTLib()
	if len(data) == 0 {
		return "", fmt.Errorf("%w (%s/%s): set PUREOCR_ORT_LIB",
			ErrUnsupportedPlatform, runtime.GOOS, runtime.GOARCH)
	}
	path, err := unpackLibrary(name, data)
	if err != nil {
		return "", fmt.Errorf("pureocr: unpack ONNX Runtime: %w", err)
	}
	return path, nil
}

// unpackLibrary writes the embedded library to a per-user cache directory once
// and returns its path. A concurrent writer is harmless: both write identical
// bytes and the rename is atomic.
func unpackLibrary(name string, data []byte) (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	dir = filepath.Join(dir, "pureocr")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if st, err := os.Stat(path); err == nil && st.Size() == int64(len(data)) {
		return path, nil
	}
	tmp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		// Another process won the race; use whatever is there.
		if st, serr := os.Stat(path); serr == nil && st.Size() == int64(len(data)) {
			os.Remove(tmp.Name())
			return path, nil
		}
		os.Remove(tmp.Name())
		return "", err
	}
	return path, nil
}
