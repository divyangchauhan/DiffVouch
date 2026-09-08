package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/divyangchauhan/DiffVouch/internal/config"
	"github.com/divyangchauhan/DiffVouch/internal/dv"
	"github.com/divyangchauhan/DiffVouch/internal/model"
	"github.com/divyangchauhan/DiffVouch/internal/secret"
)

type Prompt struct {
	System string
	User   string
}

type Adapter interface {
	Review(Prompt) (model.ProviderReview, error)
	Model() string
}

type Options struct {
	Name      string
	Transport string
	Model     string
	Effort    string
}

func New(options Options) (Adapter, error) {
	switch options.Name + "/" + options.Transport {
	case "codex/cli":
		return &cliAdapter{name: "codex", executable: "codex", model: options.Model, effort: options.Effort}, nil
	case "claude/cli":
		return &cliAdapter{name: "claude", executable: "claude", model: options.Model, effort: options.Effort}, nil
	case "codex/api":
		if options.Model == "" {
			return nil, dv.New(dv.ExitProvider, "OpenAI API transport requires --model or a trusted repository model")
		}
		return &apiAdapter{name: "codex", model: options.Model, effort: options.Effort}, nil
	case "claude/api":
		if options.Model == "" {
			return nil, dv.New(dv.ExitProvider, "Anthropic API transport requires --model or a trusted repository model")
		}
		return &apiAdapter{name: "claude", model: options.Model, effort: options.Effort}, nil
	default:
		return nil, dv.New(dv.ExitProvider, "unsupported provider/transport combination")
	}
}

type cliAdapter struct {
	name       string
	executable string
	model      string
	effort     string
}

func (a *cliAdapter) Model() string {
	if a.model != "" {
		return a.model
	}
	if a.name == "codex" {
		return "default (Codex CLI)"
	}
	return "default (Claude Code)"
}

func (a *cliAdapter) Review(prompt Prompt) (model.ProviderReview, error) {
	if _, err := exec.LookPath(a.executable); err != nil {
		return model.ProviderReview{}, dv.New(dv.ExitProvider, a.executable+" CLI is not installed")
	}
	context, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if a.name == "codex" {
		status := exec.CommandContext(context, "codex", "login", "status")
		if err := status.Run(); err != nil {
			return model.ProviderReview{}, dv.New(dv.ExitProvider, "Codex CLI is not authenticated; run 'diffvouch auth login openai'")
		}
		return a.reviewCodex(context, prompt)
	}
	status := exec.CommandContext(context, "claude", "auth", "status")
	if err := status.Run(); err != nil {
		return model.ProviderReview{}, dv.New(dv.ExitProvider, "Claude Code is not authenticated; run 'diffvouch auth login claude'")
	}
	return a.reviewClaude(context, prompt)
}

func (a *cliAdapter) reviewCodex(ctx context.Context, prompt Prompt) (model.ProviderReview, error) {
	directory, err := os.MkdirTemp("", "diffvouch-codex-")
	if err != nil {
		return model.ProviderReview{}, dv.Wrap(dv.ExitProvider, "create provider workspace", err)
	}
	defer os.RemoveAll(directory)
	schemaPath := filepath.Join(directory, "review-schema.json")
	outputPath := filepath.Join(directory, "review.json")
	if err := os.WriteFile(schemaPath, schemaJSON(), 0o600); err != nil {
		return model.ProviderReview{}, dv.Wrap(dv.ExitProvider, "write provider schema", err)
	}
	args := []string{
		"exec", "--skip-git-repo-check", "--ephemeral", "--ignore-user-config",
		"--disable", "shell_tool", "--disable", "apps", "--disable", "multi_agent",
		"--config", `web_search="disabled"`,
		"--config", "developer_instructions=" + mustJSON(prompt.System),
		"--sandbox", "read-only", "--output-schema", schemaPath,
		"--output-last-message", outputPath,
	}
	if a.model != "" {
		args = append(args, "--model", a.model)
	}
	if a.effort != "" {
		args = append(args, "--config", "model_reasoning_effort="+mustJSON(a.effort))
	}
	args = append(args, "-")
	command := exec.CommandContext(ctx, "codex", args...)
	command.Dir = directory
	command.Stdin = strings.NewReader(prompt.User)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return model.ProviderReview{}, providerCommandError("Codex review failed", stderr.String(), err)
	}
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		return model.ProviderReview{}, dv.Wrap(dv.ExitProvider, "Codex did not write structured output", err)
	}
	return decodeReview(raw)
}

func (a *cliAdapter) reviewClaude(ctx context.Context, prompt Prompt) (model.ProviderReview, error) {
	args := []string{
		"--safe-mode", "--tools", "", "--disallowedTools", "mcp__*",
		"--no-session-persistence", "--output-format", "json",
		"--json-schema", string(schemaJSON()), "--system-prompt", prompt.System,
	}
	if a.model != "" {
		args = append(args, "--model", a.model)
	}
	if a.effort != "" {
		args = append(args, "--effort", a.effort)
	}
	args = append(args, "-p")
	command := exec.CommandContext(ctx, "claude", args...)
	command.Stdin = strings.NewReader(prompt.User)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return model.ProviderReview{}, providerCommandError("Claude review failed", stderr.String(), err)
	}
	var envelope struct {
		Structured json.RawMessage `json:"structured_output"`
		Result     string          `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		return model.ProviderReview{}, dv.Wrap(dv.ExitProvider, "Claude returned invalid JSON", err)
	}
	if len(envelope.Structured) > 0 && string(envelope.Structured) != "null" {
		return decodeReview(envelope.Structured)
	}
	if envelope.Result != "" {
		return decodeReview([]byte(envelope.Result))
	}
	return model.ProviderReview{}, dv.New(dv.ExitProvider, "Claude completed without structured output")
}

type apiAdapter struct {
	name   string
	model  string
	effort string
}

func (a *apiAdapter) Model() string { return a.model }

func (a *apiAdapter) Review(prompt Prompt) (model.ProviderReview, error) {
	if a.name == "codex" {
		return a.reviewOpenAI(prompt)
	}
	return a.reviewAnthropic(prompt)
}

func (a *apiAdapter) reviewOpenAI(prompt Prompt) (model.ProviderReview, error) {
	body := map[string]any{
		"model": a.model,
		"input": []map[string]string{{"role": "developer", "content": prompt.System}, {"role": "user", "content": prompt.User}},
		"store": false,
		"text": map[string]any{"format": map[string]any{
			"type": "json_schema", "name": "diffvouch_review", "schema": schema(), "strict": true,
		}},
	}
	if a.effort != "" {
		body["reasoning"] = map[string]string{"effort": a.effort}
	}
	key, err := apiKey("openai")
	if err != nil {
		return model.ProviderReview{}, err
	}
	var response struct {
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := postJSON("https://api.openai.com/v1/responses", map[string]string{"Authorization": "Bearer " + key}, body, &response); err != nil {
		return model.ProviderReview{}, err
	}
	if response.Status != "completed" {
		return model.ProviderReview{}, dv.New(dv.ExitProvider, "OpenAI response did not complete")
	}
	for _, item := range response.Output {
		for _, content := range item.Content {
			if content.Type == "refusal" {
				return model.ProviderReview{}, dv.New(dv.ExitProvider, "OpenAI refused the review")
			}
			if content.Type == "output_text" {
				return decodeReview([]byte(content.Text))
			}
		}
	}
	return model.ProviderReview{}, dv.New(dv.ExitProvider, "OpenAI response contained no structured output")
}

func (a *apiAdapter) reviewAnthropic(prompt Prompt) (model.ProviderReview, error) {
	body := map[string]any{
		"model": a.model, "max_tokens": 8192, "system": prompt.System,
		"messages":      []map[string]string{{"role": "user", "content": prompt.User}},
		"output_config": map[string]any{"format": map[string]any{"type": "json_schema", "schema": schema()}},
	}
	if a.effort != "" {
		body["effort"] = a.effort
	}
	key, err := apiKey("anthropic")
	if err != nil {
		return model.ProviderReview{}, err
	}
	var response struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := postJSON("https://api.anthropic.com/v1/messages", map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01"}, body, &response); err != nil {
		return model.ProviderReview{}, err
	}
	if response.StopReason == "max_tokens" {
		return model.ProviderReview{}, dv.New(dv.ExitProvider, "Anthropic response was truncated")
	}
	for _, content := range response.Content {
		if content.Type == "text" {
			return decodeReview([]byte(content.Text))
		}
	}
	return model.ProviderReview{}, dv.New(dv.ExitProvider, "Anthropic response contained no structured output")
}

func apiKey(name string) (string, error) {
	environment := "OPENAI_API_KEY"
	if name == "anthropic" {
		environment = "ANTHROPIC_API_KEY"
	}
	if value := os.Getenv(environment); value != "" {
		return value, nil
	}
	global, err := config.LoadGlobal()
	if err != nil {
		return "", dv.Wrap(dv.ExitProvider, "load provider configuration", err)
	}
	ref, ok := global.APIKeys[name]
	if !ok {
		return "", dv.New(dv.ExitProvider, fmt.Sprintf("no %s API key configured; run 'diffvouch auth set-key %s'", name, name))
	}
	value, err := secret.Read(ref)
	if err != nil {
		return "", dv.Wrap(dv.ExitProvider, "read provider API key", err)
	}
	return value, nil
}

func postJSON(url string, headers map[string]string, body any, target any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return dv.Wrap(dv.ExitProvider, "encode provider request", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return dv.Wrap(dv.ExitProvider, "create provider request", err)
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return dv.Wrap(dv.ExitProvider, "provider request failed", err)
	}
	defer response.Body.Close()
	responseRaw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return dv.Wrap(dv.ExitProvider, "read provider response", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return dv.New(dv.ExitProvider, fmt.Sprintf("provider returned HTTP %d: %s", response.StatusCode, safeDetail(string(responseRaw))))
	}
	if err := json.Unmarshal(responseRaw, target); err != nil {
		return dv.Wrap(dv.ExitProvider, "decode provider response", err)
	}
	return nil
}

func decodeReview(raw []byte) (model.ProviderReview, error) {
	var review model.ProviderReview
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&review); err != nil {
		return review, dv.Wrap(dv.ExitProvider, "provider returned invalid structured output", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return review, dv.New(dv.ExitProvider, "provider returned trailing data after the structured review")
	}
	if err := validateReview(review); err != nil {
		return review, dv.Wrap(dv.ExitProvider, "provider returned invalid review", err)
	}
	return review, nil
}

func validateReview(review model.ProviderReview) error {
	if strings.TrimSpace(review.Summary) == "" {
		return errors.New("summary is empty")
	}
	if review.Findings == nil || review.PositiveObservations == nil || review.NeedsVerification == nil {
		return errors.New("review array fields must be JSON arrays, not null")
	}
	values := []float64{review.Dimensions.Correctness, review.Dimensions.Security, review.Dimensions.Maintainability, review.Dimensions.Testing, review.Dimensions.Scope}
	for _, value := range values {
		if value < 1 || value > 5 {
			return errors.New("dimension score must be between 1 and 5")
		}
	}
	for _, finding := range review.Findings {
		if finding.Title == "" || finding.Explanation == "" || finding.Recommendation == "" || finding.Evidence == "" {
			return errors.New("finding text fields cannot be empty")
		}
		if finding.Severity != model.Critical && finding.Severity != model.High && finding.Severity != model.Medium && finding.Severity != model.Low {
			return fmt.Errorf("invalid severity %q", finding.Severity)
		}
		if finding.Confidence != "high" && finding.Confidence != "medium" && finding.Confidence != "low" {
			return fmt.Errorf("invalid confidence %q", finding.Confidence)
		}
		if finding.Category != "correctness" && finding.Category != "security" && finding.Category != "maintainability" && finding.Category != "testing" && finding.Category != "scope" {
			return fmt.Errorf("invalid category %q", finding.Category)
		}
		if finding.Line != nil && *finding.Line < 1 {
			return errors.New("finding line must be positive")
		}
		if finding.Path != nil && *finding.Path == "" {
			return errors.New("finding path cannot be empty")
		}
		if finding.Side != nil && *finding.Side != "old" && *finding.Side != "new" {
			return errors.New("finding side must be old or new")
		}
		if finding.Line != nil && (finding.Path == nil || finding.Side == nil) {
			return errors.New("line-level finding requires path and side")
		}
		if finding.Side != nil && (finding.Path == nil || finding.Line == nil) {
			return errors.New("finding side requires path and line")
		}
	}
	return nil
}

func schemaJSON() []byte {
	raw, _ := json.Marshal(schema())
	return raw
}

func schema() map[string]any {
	nullableString := map[string]any{"type": []string{"string", "null"}}
	nullableInteger := map[string]any{"type": []string{"integer", "null"}, "minimum": 1}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"summary", "dimensions", "findings", "positiveObservations", "needsVerification"},
		"properties": map[string]any{
			"summary": map[string]any{"type": "string", "minLength": 1},
			"dimensions": map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"correctness", "security", "maintainability", "testing", "scope"},
				"properties": map[string]any{
					"correctness": scoreSchema(), "security": scoreSchema(), "maintainability": scoreSchema(), "testing": scoreSchema(), "scope": scoreSchema(),
				},
			},
			"findings": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"severity", "category", "blocking", "title", "explanation", "recommendation", "path", "line", "side", "confidence", "evidence"},
				"properties": map[string]any{
					"severity": enumSchema("critical", "high", "medium", "low"),
					"category": enumSchema("correctness", "security", "maintainability", "testing", "scope"),
					"blocking": map[string]any{"type": "boolean"}, "title": textSchema(), "explanation": textSchema(), "recommendation": textSchema(),
					"path": nullableString, "line": nullableInteger,
					"side":       map[string]any{"type": []string{"string", "null"}, "enum": []any{"old", "new", nil}},
					"confidence": enumSchema("high", "medium", "low"), "evidence": textSchema(),
				},
			}},
			"positiveObservations": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"needsVerification":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
}

func scoreSchema() map[string]any {
	return map[string]any{"type": "number", "minimum": 1, "maximum": 5}
}
func textSchema() map[string]any { return map[string]any{"type": "string", "minLength": 1} }
func enumSchema(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

func providerCommandError(label, stderr string, err error) error {
	detail := safeDetail(stderr)
	if detail == "" {
		detail = err.Error()
	}
	return dv.New(dv.ExitProvider, label+": "+detail)
}

func safeDetail(value string) string {
	value = strings.TrimSpace(value)
	if index := strings.LastIndex(value, "ERROR:"); index >= 0 {
		value = value[index:]
	}
	if len(value) > 4000 {
		value = value[len(value)-4000:]
	}
	return value
}

func mustJSON(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
