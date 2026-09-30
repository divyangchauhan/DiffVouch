package render

import (
	"strings"
	"testing"

	"github.com/divyangchauhan/DiffVouch/internal/model"
)

func TestTerminalIncludesCoverageAndPublicationState(t *testing.T) {
	result := model.ReviewResult{
		Rating:  model.Rating{Overall: 4, Label: "Good", Dimensions: model.Dimensions{Correctness: 4, Security: 4, Maintainability: 4, Testing: 4, Scope: 4}},
		Summary: "Focused change.", Scope: model.Scope{Mode: "working-tree", BaseRef: "HEAD", BaseSHA: "abc", HeadSHA: "abc"},
		Provider:             model.ProviderInfo{Name: "codex", Transport: "cli", Model: "test"},
		PositiveObservations: []string{"Clear implementation."}, NeedsVerification: []string{"Run integration tests."},
		Files: model.FilesSummary{Reviewed: []string{"app.go"}, Excluded: []string{"vendor/x"}, Binary: []string{"image.bin"}},
		Gate:  model.Gate{Passed: true, Reasons: []string{}},
	}
	output := Terminal(result)
	for _, expected := range []string{"Scope: working-tree", "Positive observations", "Needs verification", "vendor/x", "image.bin", "Quality gate passed", "Nothing was published"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in:\n%s", expected, output)
		}
	}
}

func TestTerminalEscapesFindingPathControls(t *testing.T) {
	path := "forged\n\x1b[31m.go"
	result := model.ReviewResult{Findings: []model.Finding{{ID: "DV-001", Severity: model.Medium, Path: &path}}}
	output := Terminal(result)
	if strings.Contains(output, "forged\n") || strings.Contains(output, "\x1b") || !strings.Contains(output, `forged\n\x1b`) {
		t.Fatalf("unsafe path reached terminal output: %q", output)
	}
}

func TestTokenReportSeparatesContextPeakFromCumulativeUsage(t *testing.T) {
	result := model.ReviewResult{Tokens: &model.TokenReport{Tokenizer: "o200k_base", Estimated: true, ChunkLimitTokens: 50000,
		Chunks: []model.ChunkTokens{
			{DiffTokens: 30000, PromptTextTokens: 32000, Requests: []*model.TokenUsage{
				{InputTokens: 35000, OutputTokens: 1000, CachedInputTokens: 10000, ReasoningTokens: 600},
				{InputTokens: 45000, OutputTokens: 2000, CachedInputTokens: 30000, ReasoningTokens: 1500},
			}},
			{DiffTokens: 5000, PromptTextTokens: 7000, Requests: []*model.TokenUsage{nil}},
		},
	}}
	output := Terminal(result)
	for _, expected := range []string{"estimated for this model", "peak input: 45000", "80000 input (40000 cached)", "3000 output (2100 reasoning included)", "may be incomplete"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in %s", expected, output)
		}
	}
	encoded, err := JSON(result)
	if err != nil || !strings.Contains(encoded, `"requests": [`) || !strings.Contains(encoded, "null") {
		t.Fatalf("usage missing from JSON: %s %v", encoded, err)
	}
}

func TestTerminalEscapesProviderControlledText(t *testing.T) {
	result := model.ReviewResult{
		Summary: "summary\nforged\x1b]52;c;clipboard\a",
		Findings: []model.Finding{{
			ID: "DV-001", Severity: model.Medium, Title: "title\x1b[31m", Explanation: "explanation\rforged", Recommendation: "recommendation\nforged",
		}},
		PositiveObservations: []string{"positive\x1b[2J"},
		NeedsVerification:    []string{"verification\u202eforged"},
	}
	output := Terminal(result)
	if strings.ContainsAny(output, "\x1b\a\r\u202e") || strings.Contains(output, "summary\nforged") || strings.Contains(output, "recommendation\nforged") {
		t.Fatalf("provider control text reached terminal output: %q", output)
	}
	for _, escaped := range []string{`summary\nforged\x1b`, `explanation\rforged`, `recommendation\nforged`, `verification\u202e`} {
		if !strings.Contains(output, escaped) {
			t.Fatalf("missing terminal-safe text %q in %q", escaped, output)
		}
	}
}
