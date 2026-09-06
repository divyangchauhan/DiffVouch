package secret

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestFileStoreSerializesConcurrentUpdates(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const count = 24
	errors := make(chan error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			name := fmt.Sprintf("secret-%d", index)
			_, err := Store(name, "value", "file")
			errors <- err
		}(index)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	values, err := loadFile()
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != count {
		t.Fatalf("concurrent updates were lost: got %d secrets, want %d", len(values), count)
	}
}

func TestStoreRecoversFromNullFallbackMap(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, err := secretPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Store("name", "value", "file"); err != nil {
		t.Fatal(err)
	}
	value, err := Read(Ref{Name: "name", Backend: "file"})
	if err != nil || value != "value" {
		t.Fatalf("null fallback did not recover: value=%q err=%v", value, err)
	}
}
