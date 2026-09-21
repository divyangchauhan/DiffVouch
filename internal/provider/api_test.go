package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(t *testing.T, value any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}
}

func toolResponse(name string, blocks ...any) map[string]any {
	if name == "codex" {
		return map[string]any{"status": "completed", "output": blocks}
	}
	return map[string]any{"stop_reason": "tool_use", "content": blocks}
}

func toolCall(name, id, tool string, args any) map[string]any {
	if name == "codex" {
		raw, _ := json.Marshal(args)
		return map[string]any{"type": "function_call", "call_id": id, "name": tool, "arguments": string(raw)}
	}
	return map[string]any{"type": "tool_use", "id": id, "name": tool, "input": args}
}

func finalResponse(name string) map[string]any {
	raw, _ := json.Marshal(validReview())
	if name == "codex" {
		return map[string]any{"status": "completed", "output": []any{map[string]any{
			"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": string(raw)}},
		}}}
	}
	return map[string]any{"stop_reason": "end_turn", "content": []any{map[string]string{"type": "text", "text": string(raw)}}}
}

func TestAPIReviewExecutesToolsWithoutProviderCLIs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test isolates PATH with a Bash symlink")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			repo, bin, outside := t.TempDir(), t.TempDir(), t.TempDir()
			if err := os.Symlink(bash, filepath.Join(bin, "bash")); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin) // Neither provider CLI is available.
			t.Setenv("OPENAI_API_KEY", "test-key")
			t.Setenv("ANTHROPIC_API_KEY", "test-key")
			for path, text := range map[string]string{filepath.Join(repo, "context.txt"): "repository context", filepath.Join(outside, "extra.txt"): "outside context"} {
				if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			adapter, err := New(Options{Name: name, Transport: "api", Root: repo, Model: "test-model", Effort: "high"})
			if err != nil {
				t.Fatal(err)
			}
			a := adapter.(*apiAdapter)
			requests := 0
			a.client = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if _, ok := r.Context().Deadline(); !ok {
					t.Fatal("request lacks the review deadline")
				}
				var definitions []map[string]json.RawMessage
				if err := json.Unmarshal(body["tools"], &definitions); err != nil || len(definitions) != 2 {
					t.Fatalf("missing local tools: %s", body["tools"])
				}
				if name == "codex" {
					if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer test-key" || string(body["store"]) != "false" || !strings.Contains(string(body["include"]), "reasoning.encrypted_content") || !strings.Contains(string(body["text"]), "json_schema") {
						t.Fatal("invalid OpenAI request configuration")
					}
				} else if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "test-key" || !strings.Contains(string(body["output_config"]), `"effort":"high"`) || !strings.Contains(string(body["output_config"]), "json_schema") {
					t.Fatal("invalid Anthropic request configuration")
				}
				requests++
				switch requests {
				case 1:
					thinking := map[string]any{"type": "reasoning", "id": "reasoning_1", "summary": []any{}, "encrypted_content": "preserve-this"}
					preamble := map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": "Inspecting the context."}}}
					if name == "claude" {
						thinking = map[string]any{"type": "thinking", "thinking": "Check context", "signature": "preserve-this"}
						preamble = map[string]any{"type": "text", "text": "Inspecting the context."}
					}
					return jsonResponse(t, toolResponse(name, thinking, preamble,
						toolCall(name, "read-1", "read_file", map[string]any{"path": filepath.Join(outside, "extra.txt"), "offset": 0, "max_bytes": 100}),
						toolCall(name, "bash-1", "bash", map[string]any{"command": `IFS= read -r line < context.txt; printf '%s' "$line"; printf ' stderr' >&2; exit 7`, "workdir": "", "timeout_seconds": 5}),
					)), nil
				case 2:
					results := requestToolResults(t, name, body)
					if results["read-1"].Output != "outside context" || results["read-1"].Error != "" {
						t.Fatalf("file read result was lost: %#v", results)
					}
					command := results["bash-1"]
					if command.Output != "repository context stderr" || command.ExitCode == nil || *command.ExitCode != 7 || command.Error == "" {
						t.Fatalf("Bash output or exit status was lost: %#v", command)
					}
					history := body["input"]
					if name == "claude" {
						history = body["messages"]
					}
					if !strings.Contains(string(history), "preserve-this") || !strings.Contains(string(history), "Inspecting the context.") {
						t.Fatal("reasoning or assistant context was dropped")
					}
					return jsonResponse(t, toolResponse(name, toolCall(name, "bash-2", "bash", map[string]any{"command": "printf 'second command'", "workdir": "", "timeout_seconds": 5}))), nil
				case 3:
					results := requestToolResults(t, name, body)
					if results["bash-2"].Output != "second command" || results["bash-2"].Error != "" {
						t.Fatalf("second tool round failed: %#v", results)
					}
					return jsonResponse(t, finalResponse(name)), nil
				default:
					t.Fatal("unexpected extra API request")
					return nil, nil
				}
			})}
			result, err := a.Review(Prompt{System: "Review with tools.", User: "patch"})
			if err != nil || requests != 3 || result.Summary != "No issues." {
				t.Fatalf("review failed after %d requests: %#v, %v", requests, result, err)
			}
		})
	}
}

func requestToolResults(t *testing.T, name string, body map[string]json.RawMessage) map[string]toolResult {
	t.Helper()
	results := map[string]toolResult{}
	var blocks []struct {
		Type      string          `json:"type"`
		CallID    string          `json:"call_id"`
		ToolUseID string          `json:"tool_use_id"`
		Output    string          `json:"output"`
		Content   json.RawMessage `json:"content"`
		IsError   bool            `json:"is_error"`
	}
	if name == "codex" {
		if err := json.Unmarshal(body["input"], &blocks); err != nil {
			t.Fatal(err)
		}
	} else {
		var messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(body["messages"], &messages); err != nil || len(messages) < 3 {
			t.Fatal("missing Anthropic tool conversation")
		}
		if messages[len(messages)-1].Role != "user" || messages[len(messages)-2].Role != "assistant" {
			t.Fatal("tool results do not immediately follow assistant tool use")
		}
		if err := json.Unmarshal(messages[len(messages)-1].Content, &blocks); err != nil {
			t.Fatal(err)
		}
	}
	for _, block := range blocks {
		if block.Type != "function_call_output" && block.Type != "tool_result" {
			continue
		}
		id, raw := block.CallID, block.Output
		if name == "claude" {
			id = block.ToolUseID
			if err := json.Unmarshal(block.Content, &raw); err != nil {
				t.Fatal(err)
			}
		}
		var result toolResult
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			t.Fatal(err)
		}
		if name == "claude" && block.IsError != (result.Error != "") {
			t.Fatal("tool error flag does not match the result")
		}
		results[id] = result
	}
	return results
}

func TestAPIStopsOnIncompleteResponses(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	for _, name := range []string{"codex", "claude"} {
		stops := []string{"incomplete", "failed"}
		if name == "claude" {
			stops = []string{"max_tokens", "refusal", "pause_turn"}
		}
		for _, stop := range stops {
			t.Run(name+"/"+stop, func(t *testing.T) {
				repo := t.TempDir()
				a := &apiAdapter{name: name, model: "test", root: repo}
				a.client = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
					response := toolResponse(name, toolCall(name, "no-execute", "bash", map[string]any{"command": "echo wrong > executed", "workdir": "", "timeout_seconds": 5}))
					if name == "codex" {
						response["status"] = stop
					} else {
						response["stop_reason"] = stop
					}
					return jsonResponse(t, response), nil
				})}
				if _, err := a.Review(Prompt{}); err == nil {
					t.Fatal("incomplete response accepted")
				}
				if _, err := os.Stat(filepath.Join(repo, "executed")); !os.IsNotExist(err) {
					t.Fatal("executed a tool from an incomplete response")
				}
			})
		}
	}
}

func TestAPIToolLimitRequestsFinalReview(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			requests := 0
			a := &apiAdapter{name: name, model: "test"}
			a.client = &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				requests++
				if requests <= maxToolRounds {
					return jsonResponse(t, toolResponse(name, toolCall(name, fmt.Sprint(requests), "unknown_tool", map[string]any{}))), nil
				}
				if requests != maxToolRounds+1 || !strings.Contains(string(body["tool_choice"]), "none") {
					t.Fatal("tool budget did not force a final response")
				}
				results := requestToolResults(t, name, body)
				if !strings.Contains(results[fmt.Sprint(maxToolRounds)].Error, "unknown tool") {
					t.Fatal("unknown tool error was not returned to the model")
				}
				return jsonResponse(t, finalResponse(name)), nil
			})}
			if _, err := a.Review(Prompt{}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestToolCallValidation(t *testing.T) {
	for _, ids := range [][]string{{""}, {"a", "a"}} {
		if err := validateToolCalls(0, ids, map[string]bool{}); err == nil {
			t.Fatalf("accepted invalid IDs: %v", ids)
		}
	}
	if err := validateToolCalls(0, []string{"a"}, map[string]bool{"a": true}); err == nil {
		t.Fatal("accepted replayed call")
	}
	if err := validateToolCalls(maxToolRounds, []string{"a"}, map[string]bool{}); err == nil {
		t.Fatal("accepted calls after finalization")
	}
	if err := validateToolCalls(0, make([]string, maxToolCalls+1), map[string]bool{}); err == nil {
		t.Fatal("accepted too many calls")
	}
}

func TestAPIRequestHonorsReviewCancellation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	a := &apiAdapter{client: &http.Client{Transport: testTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}}
	if err := a.postJSON(ctx, "https://example.invalid", nil, nil, &struct{}{}); err == nil {
		t.Fatal("canceled request succeeded")
	}
}
