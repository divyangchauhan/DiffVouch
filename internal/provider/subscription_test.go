package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/chatgpt"
	"github.com/divyangchauhan/DiffVouch/internal/config"
	"github.com/divyangchauhan/DiffVouch/internal/secret"
)

func subscriptionSession(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("AppData", dir)
	raw, _ := json.Marshal(chatgpt.Session{AccessToken: "subscription-access", RefreshToken: "subscription-refresh", AccountID: "account", ExpiresAt: time.Now().Add(time.Hour)})
	ref, err := secret.Store("test-subscription", string(raw), "file")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.UpdateGlobal(func(global *config.Global) error { global.ChatGPT = &ref; return nil }); err != nil {
		t.Fatal(err)
	}
}

func streamResponse(events ...any) *http.Response {
	var body strings.Builder
	for _, event := range events {
		raw, _ := json.Marshal(event)
		fmt.Fprintf(&body, "data: %s\n\n", raw)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body.String()))}
}

func TestSubscriptionReviewUsesNativeToolsAndOpaqueHistory(t *testing.T) {
	subscriptionSession(t)
	t.Setenv("PATH", "") // No Codex or Claude subprocess can run.
	t.Setenv("OPENAI_API_KEY", "must-not-be-used")
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "context.txt"), []byte("repository evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := New(Options{Name: "codex", Transport: "subscription", Root: repo, Model: "subscription-model", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	a := adapter.(*apiAdapter)
	calls := 0
	a.client = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != subscriptionEndpoint || r.Header.Get("Authorization") != "Bearer subscription-access" || r.Header.Get("ChatGPT-Account-ID") != "account" || r.Header.Get("Accept") != "text/event-stream" {
			t.Fatal("incorrect subscription routing")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if string(body["stream"]) != "true" || string(body["store"]) != "false" || string(body["instructions"]) != `"system instructions"` || !strings.Contains(string(body["text"]), "json_schema") {
			t.Fatal("incorrect subscription payload")
		}
		if calls == 1 {
			return streamResponse(
				map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "reasoning", "encrypted_content": "opaque-context", "summary": []any{}}},
				map[string]any{"type": "response.output_item.done", "output_index": 1, "item": toolCall("codex", "read-1", "read_file", map[string]any{"path": "context.txt", "offset": 0, "max_bytes": 100})},
				map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}},
			), nil
		}
		if calls != 2 {
			t.Fatal("unexpected extra model call")
		}
		results := requestToolResults(t, "codex", body)
		if results["read-1"].Output != "repository evidence" || !strings.Contains(string(body["input"]), "opaque-context") {
			t.Fatal("lost native tool result or reasoning history")
		}
		return streamResponse(map[string]any{"type": "response.completed", "response": finalResponse("codex")}), nil
	})}
	result, err := a.Review(Prompt{System: "system instructions", User: "patch"})
	if err != nil || result.Summary != "No issues." || calls != 2 {
		t.Fatalf("native subscription review failed: %v", err)
	}
}

func TestSubscriptionRefreshesOnceAfter401(t *testing.T) {
	subscriptionSession(t)
	attempts, refreshes := 0, 0
	claims, _ := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "account"}})
	newToken := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	a := &apiAdapter{name: "codex", subscription: true}
	a.client = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "auth.openai.com" {
			refreshes++
			if refreshes > 1 {
				t.Fatal("unbounded token refresh")
			}
			return jsonResponse(t, map[string]string{"access_token": newToken, "refresh_token": "rotated"}), nil
		}
		attempts++
		if attempts == 1 {
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("expired"))}, nil
		}
		if attempts != 2 || r.Header.Get("Authorization") != "Bearer "+newToken {
			t.Fatal("did not retry with new token")
		}
		return streamResponse(map[string]any{"type": "response.completed", "response": finalResponse("codex")}), nil
	})}
	_, err := a.postSubscription(context.Background(), map[string]any{"model": "test"})
	if err != nil || attempts != 2 || refreshes != 1 {
		t.Fatalf("refresh recovery failed: %v", err)
	}
}

func TestSubscriptionAcceptsStreamWithoutContentType(t *testing.T) {
	subscriptionSession(t)
	a := &apiAdapter{name: "codex", subscription: true}
	a.client = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		completed, _ := json.Marshal(map[string]any{"type": "response.completed", "response": finalResponse("codex")})
		body := "event: response.created\ndata: {\"type\":\"response.created\"}\n\nevent: response.completed\ndata: " + string(completed) + "\n\n"
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	result, err := a.postSubscription(context.Background(), map[string]any{"model": "test"})
	if err != nil || result.Status != "completed" || len(result.Output) != 1 {
		t.Fatalf("valid untyped event stream rejected: %v", err)
	}
}

func TestSubscriptionLimitsDoNotFallBackToAPI(t *testing.T) {
	subscriptionSession(t)
	t.Setenv("OPENAI_API_KEY", "billable-key-must-not-be-used")
	a := &apiAdapter{name: "codex", subscription: true, model: "test"}
	calls := 0
	a.client = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "chatgpt.com" {
			t.Fatal("subscription fell back to another endpoint")
		}
		return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("limit"))}, nil
	})}
	_, err := a.Review(Prompt{System: "review", User: "patch"})
	if err == nil || calls != 1 || !strings.Contains(err.Error(), "usage limit") {
		t.Fatalf("incorrect limit behavior: %v", err)
	}
}

func TestSubscriptionStreamRequiresCompletion(t *testing.T) {
	for name, body := range map[string]string{
		"disconnect": "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\"}}\n\n",
		"failed":     "data: {\"type\":\"response.failed\"}\n\n",
		"incomplete": "data: {\"type\":\"response.incomplete\"}\n\n",
		"bad JSON":   "data: broken\n\n",
		"done only":  "data: [DONE]\n\n",
		"oversized":  "data: " + strings.Repeat("x", 4<<20) + "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readSubscriptionStream(strings.NewReader(body)); err == nil {
				t.Fatal("accepted invalid stream")
			}
		})
	}
	body := ": keepalive\r\nevent: response.completed\r\ndata: {\"type\":\"response.completed\",\r\ndata: \"response\":{\"status\":\"completed\",\"output\":[]}}"
	if _, err := readSubscriptionStream(strings.NewReader(body)); err != nil {
		t.Fatalf("multiline CRLF event failed: %v", err)
	}
}

func TestSubscriptionRequiresExplicitModelAndOpenAI(t *testing.T) {
	for _, opts := range []Options{{Name: "codex", Transport: "subscription"}, {Name: "claude", Transport: "subscription", Model: "model"}} {
		if _, err := New(opts); err == nil {
			t.Fatal("accepted unsupported subscription options")
		}
	}
}

func TestSubscriptionChecksBudgetBeforeEveryModelCall(t *testing.T) {
	subscriptionSession(t)
	a := &apiAdapter{name: "codex", subscription: true, model: "test", root: t.TempDir()}
	checks, requests := 0, 0
	a.beforeCall = func(context.Context) error {
		checks++
		if checks > 1 {
			return chatgpt.ErrAllowance
		}
		return nil
	}
	a.client = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		return streamResponse(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "output": []any{toolCall("codex", "read-1", "read_file", map[string]any{"path": "missing", "offset": 0, "max_bytes": 100})}}}), nil
	})}
	_, err := a.Review(Prompt{System: "review", User: "patch"})
	if !errors.Is(err, chatgpt.ErrAllowance) || checks != 2 || requests != 1 {
		t.Fatalf("budget failed: checks=%d requests=%d error=%v", checks, requests, err)
	}
}
