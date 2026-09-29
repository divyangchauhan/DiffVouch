//go:build unix

package provider

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestBashTimeoutStopsDescendants(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("Bash not installed")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not installed")
	}
	repo := t.TempDir()
	result := runTestTool(context.Background(), repo, "bash", map[string]any{
		"command": "(sleep 2; echo leaked > descendant-ran) & wait", "workdir": "", "timeout_seconds": 1,
	})
	if !result.TimedOut {
		t.Fatalf("command did not time out: %#v", result)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(repo, "descendant-ran")); !os.IsNotExist(err) {
		t.Fatal("a descendant continued after command timeout")
	}
}
