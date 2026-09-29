package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/chatgpt"
	"github.com/divyangchauhan/DiffVouch/internal/config"
	"github.com/divyangchauhan/DiffVouch/internal/secret"
)

func TestNativeAuthStatusAndLogoutWithoutProviderCLIs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("AppData", dir)
	t.Setenv("PATH", "")
	raw, _ := json.Marshal(chatgpt.Session{AccessToken: "access", RefreshToken: "refresh", AccountID: "account", ExpiresAt: time.Now().Add(time.Hour)})
	ref, err := secret.Store("session", string(raw), "file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.UpdateGlobal(func(global *config.Global) error { global.ChatGPT = &ref; return nil }); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	status := authStatusCommand()
	status.SetOut(&output)
	status.SetArgs([]string{"openai", "--transport", "subscription"})
	if err := status.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "signed in") || strings.Contains(output.String(), "access") || strings.Contains(output.String(), "refresh") {
		t.Fatalf("bad status output: %s", output.String())
	}
	logout := authLogoutCommand()
	logout.SetOut(&output)
	logout.SetArgs([]string{"openai", "--transport", "subscription"})
	if err := logout.Execute(); err != nil {
		t.Fatal(err)
	}
	if _, err := chatgpt.Status(); err == nil {
		t.Fatal("session remains after logout")
	}
}

func TestSubscriptionRejectsUnsupportedProviderBeforeReview(t *testing.T) {
	command := reviewCommand()
	command.SetArgs([]string{"--provider", "claude", "--transport", "subscription", "--model", "model"})
	command.SetErr(&bytes.Buffer{})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "supports only") {
		t.Fatalf("wrong validation: %v", err)
	}
}
