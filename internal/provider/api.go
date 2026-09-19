package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/dv"
	"github.com/divyangchauhan/DiffVouch/internal/model"
)

const (
	maxToolRounds       = 32
	maxToolCalls        = 128
	finalizeInstruction = "The tool execution budget has been reached. Finish the structured review using the evidence already collected, and include any checks you could not complete in needsVerification."
)

type apiAdapter struct {
	name, model, effort, root string
	client                    *http.Client
}

func (a *apiAdapter) Model() string { return a.model }

func (a *apiAdapter) Review(prompt Prompt) (model.ProviderReview, error) {
	interruptCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(interruptCtx, 10*time.Minute)
	defer cancel()
	if a.name == "codex" {
		return a.reviewOpenAI(ctx, prompt)
	}
	return a.reviewAnthropic(ctx, prompt)
}

// Decode only the fields needed for dispatch. Replay the original JSON blocks
// to preserve reasoning, encrypted content, thinking signatures, and tool IDs.
type apiBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Text      string          `json:"text"`
	Content   []apiBlock      `json:"content"`
}

func (a *apiAdapter) reviewOpenAI(ctx context.Context, prompt Prompt) (model.ProviderReview, error) {
	key, err := apiKey("openai")
	if err != nil {
		return model.ProviderReview{}, err
	}
	input := []any{map[string]string{"role": "developer", "content": prompt.System}, map[string]string{"role": "user", "content": prompt.User}}
	tools := make([]map[string]any, 0, 2)
	for _, tool := range reviewTools() {
		tools = append(tools, map[string]any{"type": "function", "name": tool.name, "description": tool.description, "parameters": tool.parameters, "strict": true})
	}
	body := map[string]any{
		"model": a.model, "store": false, "tools": tools,
		"include": []string{"reasoning.encrypted_content"},
		"text": map[string]any{"format": map[string]any{
			"type": "json_schema", "name": "diffvouch_review", "schema": schema(), "strict": true,
		}},
	}
	if a.effort != "" {
		body["reasoning"] = map[string]string{"effort": a.effort}
	}
	seen := map[string]bool{}
	for round := 0; round <= maxToolRounds; round++ {
		if round == maxToolRounds || len(seen) >= maxToolCalls {
			body["tool_choice"] = "none"
			input = append(input, map[string]string{"role": "developer", "content": finalizeInstruction})
		}
		body["input"] = input
		var response struct {
			Status string            `json:"status"`
			Output []json.RawMessage `json:"output"`
		}
		if err := a.postJSON(ctx, "https://api.openai.com/v1/responses", map[string]string{"Authorization": "Bearer " + key}, body, &response); err != nil {
			return model.ProviderReview{}, err
		}
		if response.Status != "completed" {
			return model.ProviderReview{}, dv.New(dv.ExitProvider, "OpenAI response did not complete")
		}
		var calls []apiBlock
		var text strings.Builder
		for _, raw := range response.Output {
			var block apiBlock
			if err := json.Unmarshal(raw, &block); err != nil {
				return model.ProviderReview{}, dv.Wrap(dv.ExitProvider, "decode OpenAI output item", err)
			}
			if block.Type == "function_call" {
				calls = append(calls, block)
			}
			for _, content := range block.Content {
				if content.Type == "refusal" {
					return model.ProviderReview{}, dv.New(dv.ExitProvider, "OpenAI refused the review")
				}
				if content.Type == "output_text" {
					text.WriteString(content.Text)
				}
			}
			input = append(input, raw)
		}
		if len(calls) == 0 {
			if text.Len() == 0 {
				return model.ProviderReview{}, dv.New(dv.ExitProvider, "OpenAI response contained no structured output")
			}
			return decodeReview([]byte(text.String()))
		}
		ids := make([]string, len(calls))
		for i, call := range calls {
			ids[i] = call.CallID
		}
		if err := validateToolCalls(round, ids, seen); err != nil {
			return model.ProviderReview{}, err
		}
		for _, call := range calls {
			result := executeTool(ctx, a.root, call.Name, json.RawMessage(call.Arguments))
			input = append(input, map[string]any{"type": "function_call_output", "call_id": call.CallID, "output": result.json()})
		}
	}
	return model.ProviderReview{}, dv.New(dv.ExitProvider, "OpenAI review exceeded the tool round limit")
}

func (a *apiAdapter) reviewAnthropic(ctx context.Context, prompt Prompt) (model.ProviderReview, error) {
	key, err := apiKey("anthropic")
	if err != nil {
		return model.ProviderReview{}, err
	}
	messages := []any{map[string]string{"role": "user", "content": prompt.User}}
	tools := make([]map[string]any, 0, 2)
	for _, tool := range reviewTools() {
		tools = append(tools, map[string]any{"name": tool.name, "description": tool.description, "input_schema": tool.parameters})
	}
	outputConfig := map[string]any{"format": map[string]any{"type": "json_schema", "schema": schema()}}
	if a.effort != "" {
		outputConfig["effort"] = a.effort
	}
	body := map[string]any{"model": a.model, "max_tokens": 8192, "system": prompt.System, "tools": tools, "output_config": outputConfig}
	seen := map[string]bool{}
	for round := 0; round <= maxToolRounds; round++ {
		if round == maxToolRounds || len(seen) >= maxToolCalls {
			body["tool_choice"] = map[string]string{"type": "none"}
			body["system"] = prompt.System + "\n" + finalizeInstruction
		}
		body["messages"] = messages
		var response struct {
			StopReason string            `json:"stop_reason"`
			Content    []json.RawMessage `json:"content"`
		}
		if err := a.postJSON(ctx, "https://api.anthropic.com/v1/messages", map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01"}, body, &response); err != nil {
			return model.ProviderReview{}, err
		}
		if response.StopReason != "end_turn" && response.StopReason != "tool_use" {
			return model.ProviderReview{}, dv.New(dv.ExitProvider, "Anthropic review did not complete: "+response.StopReason)
		}
		var calls []apiBlock
		var text strings.Builder
		for _, raw := range response.Content {
			var block apiBlock
			if err := json.Unmarshal(raw, &block); err != nil {
				return model.ProviderReview{}, dv.Wrap(dv.ExitProvider, "decode Anthropic content block", err)
			}
			if block.Type == "tool_use" {
				calls = append(calls, block)
			}
			if block.Type == "text" {
				text.WriteString(block.Text)
			}
		}
		if response.StopReason == "end_turn" && len(calls) == 0 {
			if text.Len() == 0 {
				return model.ProviderReview{}, dv.New(dv.ExitProvider, "Anthropic response contained no structured output")
			}
			return decodeReview([]byte(text.String()))
		}
		if response.StopReason != "tool_use" || len(calls) == 0 {
			return model.ProviderReview{}, dv.New(dv.ExitProvider, "Anthropic returned inconsistent tool use")
		}
		ids := make([]string, len(calls))
		for i, call := range calls {
			ids[i] = call.ID
		}
		if err := validateToolCalls(round, ids, seen); err != nil {
			return model.ProviderReview{}, err
		}
		messages = append(messages, map[string]any{"role": "assistant", "content": response.Content})
		results := make([]map[string]any, 0, len(calls))
		for _, call := range calls {
			result := executeTool(ctx, a.root, call.Name, call.Input)
			results = append(results, map[string]any{"type": "tool_result", "tool_use_id": call.ID, "content": result.json(), "is_error": result.Error != ""})
		}
		// All results immediately follow their assistant tool-use message.
		messages = append(messages, map[string]any{"role": "user", "content": results})
	}
	return model.ProviderReview{}, dv.New(dv.ExitProvider, "Anthropic review exceeded the tool round limit")
}

func validateToolCalls(round int, ids []string, seen map[string]bool) error {
	if round >= maxToolRounds || len(seen)+len(ids) > maxToolCalls {
		return dv.New(dv.ExitProvider, "review exceeded the tool execution limit")
	}
	for _, id := range ids {
		if id == "" || seen[id] {
			return dv.New(dv.ExitProvider, "provider returned a missing or duplicate tool call ID")
		}
		seen[id] = true
	}
	return nil
}

func (a *apiAdapter) postJSON(ctx context.Context, url string, headers map[string]string, body any, target any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return dv.Wrap(dv.ExitProvider, "encode provider request", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return dv.Wrap(dv.ExitProvider, "create provider request", err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	client := a.client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return dv.Wrap(dv.ExitProvider, "provider request failed", err)
	}
	defer response.Body.Close()
	const maxResponseBytes = 4 << 20
	responseRaw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return dv.Wrap(dv.ExitProvider, "read provider response", err)
	}
	if len(responseRaw) > maxResponseBytes {
		return dv.New(dv.ExitProvider, "provider response exceeded 4 MiB")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return dv.New(dv.ExitProvider, fmt.Sprintf("provider returned HTTP %d: %s", response.StatusCode, safeDetail(string(responseRaw))))
	}
	if err := json.Unmarshal(responseRaw, target); err != nil {
		return dv.Wrap(dv.ExitProvider, "decode provider response", err)
	}
	return nil
}
