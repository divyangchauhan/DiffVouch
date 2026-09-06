package gitdiff

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repository(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	commands := [][]string{{"init", "-q", "--initial-branch=main"}, {"config", "user.name", "DiffVouch Test"}, {"config", "user.email", "test@example.invalid"}}
	for _, args := range commands {
		command := exec.Command("git", args...)
		command.Dir = directory
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(directory, "tracked.txt"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "tracked.txt"}, {"commit", "-q", "-m", "initial"}} {
		command := exec.Command("git", args...)
		command.Dir = directory
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return directory
}

func TestCollectWorkingTreeIncludesTrackedUntrackedAndSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink privileges vary on Windows")
	}
	repo := repository(t)
	_ = os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("changed\n"), 0o600)
	_ = os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new\n"), 0o600)
	if err := os.Symlink("target\ndiff --git a/fake b/fake", filepath.Join(repo, "link.txt")); err != nil {
		t.Fatal(err)
	}
	result, err := Collect(Options{Root: repo, MaxDiffBytes: 500_000})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.ReviewedFiles, ",") != "link.txt,new.txt,tracked.txt" {
		t.Fatalf("unexpected files: %v", result.ReviewedFiles)
	}
	if !strings.Contains(result.Patch, "+diff --git a/fake b/fake") || !strings.Contains(result.Patch, "new file mode 120000") {
		t.Fatalf("symlink patch was not serialized safely:\n%s", result.Patch)
	}
}

func TestCollectReportsTrackedBinary(t *testing.T) {
	repo := repository(t)
	path := filepath.Join(repo, "binary.bin")
	_ = os.WriteFile(path, []byte("before\x00data"), 0o600)
	for _, args := range [][]string{{"add", "binary.bin"}, {"commit", "-q", "-m", "binary"}} {
		command := exec.Command("git", args...)
		command.Dir = repo
		if err := command.Run(); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(path, []byte("after\x00data"), 0o600)
	result, err := Collect(Options{Root: repo, MaxDiffBytes: 500_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.BinaryFiles) != 1 || result.BinaryFiles[0] != "binary.bin" || result.Patch != "" {
		t.Fatalf("binary coverage mismatch: %#v", result)
	}
}

func TestStagedOnlyAndExclusions(t *testing.T) {
	repo := repository(t)
	_ = os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("staged\n"), 0o600)
	command := exec.Command("git", "add", "tracked.txt")
	command.Dir = repo
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("unstaged\n"), 0o600)
	result, err := Collect(Options{Root: repo, StagedOnly: true, MaxDiffBytes: 500_000})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Patch, "+staged") || strings.Contains(result.Patch, "+unstaged") {
		t.Fatalf("wrong staged patch: %s", result.Patch)
	}
	excluded, err := Collect(Options{Root: repo, Excludes: []string{"**/*.txt", "*.txt"}, MaxDiffBytes: 500_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(excluded.ExcludedFiles) != 1 || excluded.ExcludedFiles[0] != "tracked.txt" {
		t.Fatalf("exclude did not match: %#v", excluded.ExcludedFiles)
	}
}

func TestDiffLimitFailsClosed(t *testing.T) {
	repo := repository(t)
	_ = os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte(strings.Repeat("x", 20_000)), 0o600)
	if _, err := Collect(Options{Root: repo, MaxDiffBytes: 10_000}); err == nil {
		t.Fatal("expected bounded collection failure")
	}
}

func TestChunkPatchPreservesSections(t *testing.T) {
	a := "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1 +1 @@\n-old\n+new\n"
	b := "diff --git a/b b/b\n--- a/b\n+++ b/b\n@@ -1 +1 @@\n-old\n+new\n"
	chunks, err := ChunkPatch(a+b, len(a)+1)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || chunks[0] != a || chunks[1] != b {
		t.Fatalf("unexpected chunks: %#v", chunks)
	}
}

func TestDisplayPathEscapesTerminalAndMarkdownControls(t *testing.T) {
	if got := DisplayPath("src/main.go"); got != "src/main.go" {
		t.Fatalf("safe path changed: %q", got)
	}
	got := DisplayPath("bad\n\x1b[31m`name")
	if strings.ContainsAny(got, "\n\x1b`") || !strings.Contains(got, `\n`) || !strings.Contains(got, `\u0060`) {
		t.Fatalf("unsafe path was not escaped: %q", got)
	}
}

func TestUnbornRepositoryReviewsTrackedAndUntrackedFiles(t *testing.T) {
	repo := t.TempDir()
	command := exec.Command("git", "init", "-q", "--initial-branch=main")
	command.Dir = repo
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("staged\n"), 0o600)
	command = exec.Command("git", "add", "staged.txt")
	command.Dir = repo
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new\n"), 0o600)
	result, err := Collect(Options{Root: repo, MaxDiffBytes: 500_000})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(result.ReviewedFiles, ",") != "new.txt,staged.txt" || result.BaseRef != "empty-tree" {
		t.Fatalf("unexpected unborn result: %#v", result)
	}
}
