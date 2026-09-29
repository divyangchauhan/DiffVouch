package provider

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func runTestTool(ctx context.Context, root, name string, args any) toolResult {
	raw, _ := json.Marshal(args)
	return executeTool(ctx, root, name, raw)
}

func TestReadFilePagesAndUnrestrictedPaths(t *testing.T) {
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "context.txt")
	if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{path, filepath.Join("..", "context.txt")} {
		first := runTestTool(context.Background(), repo, "read_file", map[string]any{"path": path, "offset": 0, "max_bytes": 3})
		last := runTestTool(context.Background(), repo, "read_file", map[string]any{"path": path, "offset": 3, "max_bytes": 3})
		if first.Output != "abc" || !first.Truncated || first.Error != "" || last.Output != "def" || last.Truncated || last.Error != "" {
			t.Fatalf("paging failed: first=%#v last=%#v", first, last)
		}
	}
}

func TestToolsRejectInvalidArguments(t *testing.T) {
	for _, test := range []struct{ name, raw string }{
		{"bash", `{"command":"echo should-not-run","timeout_seconds":0}`},
		{"bash", `{"command":"echo should-not-run","timeout_seconds":121}`},
		{"bash", `{"command":"echo should-not-run","timeout_seconds":1,"extra":true}`},
		{"bash", `{"command":"echo should-not-run","timeout_seconds":1} {}`},
		{"bash", `{"command":`},
		{"read_file", `{"path":"a","offset":-1,"max_bytes":10}`},
		{"read_file", `{"path":"a","offset":0,"max_bytes":65537}`},
		{"unknown", `{}`},
	} {
		if result := executeTool(context.Background(), t.TempDir(), test.name, json.RawMessage(test.raw)); result.Error == "" || result.Output != "" {
			t.Fatalf("invalid %s arguments executed: %s, %#v", test.name, test.raw, result)
		}
	}
}

func TestBashOutputLimitAndWorkingDirectory(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("Bash not installed")
	}
	repo := t.TempDir()
	subdir := filepath.Join(repo, "sub dir")
	if err := os.Mkdir(subdir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "marker"), []byte("exists"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := runTestTool(context.Background(), repo, "bash", map[string]any{
		"command": "test -f marker || exit 9; printf '%070000d' 0", "workdir": "sub dir", "timeout_seconds": 5,
	})
	if result.Error != "" || len(result.Output) != maxToolOutput || !result.Truncated || result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("Bash result: len=%d truncated=%t error=%s exit=%v", len(result.Output), result.Truncated, result.Error, result.ExitCode)
	}
}

func TestBashTimeoutAndCancellation(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("Bash not installed")
	}
	start := time.Now()
	result := runTestTool(context.Background(), t.TempDir(), "bash", map[string]any{
		"command": "while :; do :; done", "workdir": "", "timeout_seconds": 1,
	})
	if !result.TimedOut || result.Error == "" || time.Since(start) > 4*time.Second {
		t.Fatalf("command timeout failed: %#v", result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result = runTestTool(ctx, t.TempDir(), "bash", map[string]any{"command": "echo should-not-run", "workdir": "", "timeout_seconds": 1})
	if !strings.Contains(result.Error, "canceled") || result.Output != "" {
		t.Fatalf("canceled tool ran: %#v", result)
	}
}

func TestMissingBashIsReturnedAsToolError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	result := runTestTool(context.Background(), t.TempDir(), "bash", map[string]any{"command": "true", "workdir": "", "timeout_seconds": 1})
	if result.Error == "" || result.ExitCode != nil {
		t.Fatalf("missing Bash was not reported: %#v", result)
	}
}
