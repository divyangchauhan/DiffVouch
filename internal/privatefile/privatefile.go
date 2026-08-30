package privatefile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

func Write(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(temporary, 0o600)
	if runtime.GOOS != "windows" {
		if err := os.Rename(temporary, path); err != nil {
			_ = os.Remove(temporary)
			return err
		}
		return os.Chmod(path, 0o600)
	}
	backup := path + ".bak"
	_ = os.Remove(backup)
	hadPrevious := false
	if _, err := os.Stat(path); err == nil {
		if err := os.Rename(path, backup); err != nil {
			_ = os.Remove(temporary)
			return err
		}
		hadPrevious = true
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		if hadPrevious {
			_ = os.Rename(backup, path)
		}
		return err
	}
	_ = os.Remove(backup)
	return os.Chmod(path, 0o600)
}
