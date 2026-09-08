package privatefile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gofrs/flock"
)

func Write(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	temporaryFile, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporary := temporaryFile.Name()
	defer os.Remove(temporary)
	if err := temporaryFile.Chmod(0o600); err != nil {
		_ = temporaryFile.Close()
		return err
	}
	if _, err := temporaryFile.Write(data); err != nil {
		_ = temporaryFile.Close()
		return err
	}
	if err := temporaryFile.Close(); err != nil {
		return err
	}
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

func WithLock(path string, action func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	lock := flock.New(path + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock private file: %w", err)
	}
	if !locked {
		return errors.New("timed out locking private file")
	}
	defer func() { _ = lock.Unlock() }()
	return action()
}
