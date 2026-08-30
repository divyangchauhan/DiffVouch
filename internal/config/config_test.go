package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func configRepository(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "--initial-branch=main"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.invalid"}} {
		command := exec.Command("git", args...)
		command.Dir = repo
		if err := command.Run(); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(repo, ".diffvouch.yml"), []byte("version: 1\nprovider:\n  default_transport: cli\n  models:\n    codex: trusted-model\n"), 0o600)
	command := exec.Command("git", "add", ".diffvouch.yml")
	command.Dir = repo
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("git", "commit", "-q", "-m", "config")
	command.Dir = repo
	if err := command.Run(); err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestRepositoryConfigLoadsFromTrustedCommit(t *testing.T) {
	repo := configRepository(t)
	_ = os.WriteFile(filepath.Join(repo, ".diffvouch.yml"), []byte("version: 1\nreview:\n  exclude: ['**']\n"), 0o600)
	for _, explicit := range []string{"", filepath.Join(repo, ".diffvouch.yml")} {
		value, err := LoadRepository(repo, explicit, "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		if value.Provider.Models.Codex != "trusted-model" || len(value.Review.Exclude) != 0 {
			t.Fatalf("uncommitted config took effect for %q: %#v", explicit, value)
		}
	}
}

func TestExternalExplicitConfigLoadsFromFilesystem(t *testing.T) {
	repo := configRepository(t)
	external := filepath.Join(t.TempDir(), "review.yml")
	raw := "version: 1\nprovider:\n  default_transport: cli\n  models:\n    codex: external-model\n"
	if err := os.WriteFile(external, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := LoadRepository(repo, external, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if value.Provider.Models.Codex != "external-model" {
		t.Fatalf("external explicit config was not loaded: %#v", value)
	}
}

func TestRepositoryConfigCannotSelectAPI(t *testing.T) {
	repo := configRepository(t)
	path := filepath.Join(repo, "unsafe.yml")
	_ = os.WriteFile(path, []byte("version: 1\nprovider:\n  default_transport: api\n"), 0o600)
	if _, err := LoadRepository(repo, path, "HEAD"); err == nil {
		t.Fatal("API transport should not be selectable by repository config")
	}
}

func TestGlobalConfigUses0600Fallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	value, err := LoadGlobal()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveGlobal(value); err != nil {
		t.Fatal(err)
	}
	path, _ := globalPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode is %o", info.Mode().Perm())
	}
}
