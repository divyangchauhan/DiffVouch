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
	"github.com/divyangchauhan/DiffVouch/internal/model"
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
	var usage []*model.TokenUsage
	adapter, err := New(Options{Name: "codex", Transport: "subscription", Root: repo, Model: "subscription-model", Effort: "high",
		OnUsage: func(value *model.TokenUsage) { usage = append(usage, value) }})
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
				map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed", "usage": map[string]any{
					"input_tokens": 100, "output_tokens": 10, "input_tokens_details": map[string]int{"cached_tokens": 60}, "output_tokens_details": map[string]int{"reasoning_tokens": 4},
				}}},
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
	if len(usage) != 2 || usage[0] == nil || *usage[0] != (model.TokenUsage{InputTokens: 100, OutputTokens: 10, CachedInputTokens: 60, ReasoningTokens: 4}) || usage[1] != nil {
		t.Fatalf("lost stream usage or reported missing usage as zero: %#v", usage)
	}
}

func TestSubscriptionReviewSeparatesCommentaryFromFinalAnswer(t *testing.T) {
	for _, separate := range []bool{false, true} {
		t.Run(fmt.Sprintf("separate_commentary_%v", separate), func(t *testing.T) {
			subscriptionSession(t)
			message := func(phase, text string) map[string]any {
				return map[string]any{"type": "message", "role": "assistant", "phase": phase,
					"content": []any{map[string]string{"type": "output_text", "text": text}}}
			}
			final := validReview()
			final.Summary = "Completed final review."
			raw, _ := json.Marshal(final)
			calls := 0
			a := &apiAdapter{name: "codex", subscription: true, root: t.TempDir(), model: "test"}
			a.client = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if separate && calls == 1 {
					// Even schema-valid commentary is not the completed review.
					interim, _ := json.Marshal(validReview())
					return streamResponse(map[string]any{"type": "response.completed", "response": toolResponse("codex", message("commentary", string(interim)))}), nil
				}
				if separate && !strings.Contains(string(body["input"]), `"phase":"commentary"`) {
					t.Fatal("commentary phase was lost from replayed history")
				}
				if calls > 2 {
					t.Fatal("unexpected extra model call")
				}
				return streamResponse(map[string]any{"type": "response.completed", "response": toolResponse("codex",
					message("commentary", "Inspection is complete."), message("final_answer", string(raw)))}), nil
			})}
			result, err := a.Review(Prompt{System: "Review the patch.", User: "patch"})
			if err != nil || result.Summary != final.Summary || separate && calls != 2 || !separate && calls != 1 {
				t.Fatalf("review failed or stopped at commentary: calls=%d result=%#v error=%v", calls, result, err)
			}
		})
	}
}

func TestSubscriptionCommentaryCannotBypassRoundBudget(t *testing.T) {
	subscriptionSession(t)
	calls := 0
	a := &apiAdapter{name: "codex", subscription: true, model: "test"}
	a.client = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return streamResponse(map[string]any{"type": "response.completed", "response": toolResponse("codex",
			map[string]any{"type": "message", "role": "assistant", "phase": "commentary",
				"content": []any{map[string]string{"type": "output_text", "text": "Still inspecting."}}})}), nil
	})}
	_, err := a.Review(Prompt{System: "Review the patch.", User: "patch"})
	if err == nil || !strings.Contains(err.Error(), "round limit") || calls != maxToolRounds+1 {
		t.Fatalf("commentary escaped budget: calls=%d error=%v", calls, err)
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

func TestSubscriptionFailureDiagnosticsDoNotExposeResponseContent(t *testing.T) {
	for _, tc := range []struct{ name, event, want string }{
		{"failed", `{"type":"response.failed","response":{"error":{"code":"server_error","message":"private code and subscription-access"}}}`, "response.failed; server_error"},
		{"context", `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded"}}}`, "response.failed; context_length_exceeded"},
		{"incomplete", `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`, "response.incomplete; max_output_tokens"},
		{"error", `{"type":"error","code":"rate_limit_exceeded","message":"private"}`, "error; rate_limit_exceeded"},
		{"overloaded", `{"type":"error","error":{"code":"server_is_overloaded","type":"service_unavailable_error","message":"private","headers":{"x-retry-metadata":"NO_MORE_RETRY"}}}`, "error; server_is_overloaded"},
		{"nested error", `{"type":"error","error":{"code":"server_error","message":"private"}}`, "error; server_error"},
		{"unknown", `{"type":"response.failed","response":{"error":{"code":"subscription-access","message":"private"}}}`, "response.failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := readSubscriptionStream(strings.NewReader("data: " + tc.event + "\n\n"))
			if err == nil || !strings.Contains(err.Error(), "("+tc.want+")") {
				t.Fatalf("missing safe diagnostic: %v", err)
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "subscription-access") {
				t.Fatalf("exposed response content: %v", err)
			}
			if len(result.Output) != 0 {
				t.Fatal("accepted failed output")
			}
		})
	}
}
