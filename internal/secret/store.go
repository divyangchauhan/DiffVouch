package secret

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/privatefile"
	"github.com/gofrs/flock"
	"github.com/zalando/go-keyring"
)

const service = "diffvouch"

type Ref struct {
	Name    string `json:"name"`
	Backend string `json:"backend"`
}

func configDir() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "diffvouch"), nil
}

func secretPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "secrets.json"), nil
}

func Store(name, value, backend string) (Ref, error) {
	if value == "" {
		return Ref{}, errors.New("secret cannot be empty")
	}
	if backend == "auto" || backend == "keyring" {
		if err := keyring.Set(service, name, value); err == nil {
			return Ref{Name: name, Backend: "keyring"}, nil
		} else if backend == "keyring" {
			return Ref{}, fmt.Errorf("store secret in OS credential manager: %w", err)
		}
	}
	if backend != "auto" && backend != "file" {
		return Ref{}, fmt.Errorf("unsupported secret storage %q", backend)
	}
	if err := updateFile(func(values map[string]string) { values[name] = value }); err != nil {
		return Ref{}, err
	}
	return Ref{Name: name, Backend: "file"}, nil
}

func Read(ref Ref) (string, error) {
	switch ref.Backend {
	case "keyring":
		return keyring.Get(service, ref.Name)
	case "file":
		values, err := loadFile()
		if err != nil {
			return "", err
		}
		value, ok := values[ref.Name]
		if !ok {
			return "", errors.New("secret is missing from fallback storage")
		}
		return value, nil
	default:
		return "", fmt.Errorf("unsupported secret backend %q", ref.Backend)
	}
}

func Delete(ref Ref) error {
	switch ref.Backend {
	case "keyring":
		err := keyring.Delete(service, ref.Name)
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return err
	case "file":
		return updateFile(func(values map[string]string) { delete(values, ref.Name) })
	default:
		return fmt.Errorf("unsupported secret backend %q", ref.Backend)
	}
}

func loadFile() (map[string]string, error) {
	path, err := secretPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read fallback secret file: %w", err)
	}
	values := map[string]string{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("parse fallback secret file: %w", err)
	}
	if values == nil {
		values = map[string]string{}
	}
	return values, nil
}

func updateFile(update func(map[string]string)) error {
	path, err := secretPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create fallback secret directory: %w", err)
	}
	_ = os.Chmod(filepath.Dir(path), 0o700)
	lock := flock.New(path + ".lock")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 25*time.Millisecond)
	if err != nil {
		return fmt.Errorf("lock fallback secret file: %w", err)
	}
	if !locked {
		return errors.New("timed out locking fallback secret file")
	}
	defer func() { _ = lock.Unlock() }()
	values, err := loadFile()
	if err != nil {
		return err
	}
	update(values)
	return saveFile(values)
}

func saveFile(values map[string]string) error {
	path, err := secretPath()
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	return privatefile.Write(path, append(raw, '\n'))
}
