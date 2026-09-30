package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/divyangchauhan/DiffVouch/internal/gitdiff"
	"github.com/divyangchauhan/DiffVouch/internal/model"
	"github.com/divyangchauhan/DiffVouch/internal/sanitize"
)

func JSON(result model.ReviewResult) (string, error) {
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	return string(raw) + "\n", nil
}

func Terminal(result model.ReviewResult) string {
	var output strings.Builder
	fmt.Fprintf(&output, "DiffVouch review: %.1f/5 — %s\n\n%s\n\n", result.Rating.Overall, result.Rating.Label, sanitize.TerminalText(result.Summary))
	fmt.Fprintf(&output, "Scope: %s · base %s (%s) · head %s\n\n", result.Scope.Mode, result.Scope.BaseRef, result.Scope.BaseSHA, result.Scope.HeadSHA)
	blocking := filter(result.Findings, true)
	nonBlocking := filter(result.Findings, false)
	writeFindings(&output, "Blocking issues", blocking)
	writeFindings(&output, "Non-blocking issues", nonBlocking)
	if len(result.PositiveObservations) > 0 {
		output.WriteString("Positive observations\n")
		for _, observation := range result.PositiveObservations {
			fmt.Fprintf(&output, "  - %s\n", sanitize.TerminalText(observation))
		}
		output.WriteString("\n")
	}
	if len(result.NeedsVerification) > 0 {
		output.WriteString("Needs verification\n")
		for _, item := range result.NeedsVerification {
			fmt.Fprintf(&output, "  - %s\n", sanitize.TerminalText(item))
		}
		output.WriteString("\n")
	}
	fmt.Fprintf(&output, "Rating breakdown\n  Correctness: %.1f  Security: %.1f  Maintainability: %.1f  Testing: %.1f  Scope: %.1f\n\n",
		result.Rating.Dimensions.Correctness, result.Rating.Dimensions.Security,
		result.Rating.Dimensions.Maintainability, result.Rating.Dimensions.Testing,
		result.Rating.Dimensions.Scope)
	fmt.Fprintf(&output, "Reviewed %d files with %s/%s", len(result.Files.Reviewed), result.Provider.Name, result.Provider.Transport)
	if result.Provider.Model != "" {
		fmt.Fprintf(&output, " using %s", result.Provider.Model)
	}
	output.WriteString(".\n")
	if result.Tokens != nil {
		writeTokens(&output, result.Tokens)
	}
	if result.Files.Redactions > 0 {
		fmt.Fprintf(&output, "%d potential secret(s) were redacted before provider submission.\n", result.Files.Redactions)
	}
	if len(result.Files.Binary) > 0 {
		fmt.Fprintf(&output, "Skipped binary or non-UTF-8 files: %s\n", strings.Join(displayPaths(result.Files.Binary), ", "))
	}
	if len(result.Files.Excluded) > 0 {
		fmt.Fprintf(&output, "Excluded files: %s\n", strings.Join(displayPaths(result.Files.Excluded), ", "))
	}
	if !result.Gate.Passed {
		fmt.Fprintf(&output, "Quality gate failed: %s\n", strings.Join(result.Gate.Reasons, "; "))
	} else {
		output.WriteString("Quality gate passed.\n")
	}
	if result.Publication.Published && result.Publication.URL != nil {
		fmt.Fprintf(&output, "Published by %s: %s\n", pointer(result.Publication.Bot), *result.Publication.URL)
	} else if !result.Publication.Requested {
		output.WriteString("Nothing was published.\n")
	}
	return output.String()
}

func writeTokens(output *strings.Builder, report *model.TokenReport) {
	label := "local text count"
	if report.Estimated {
		label = "estimated for this model"
	}
	fmt.Fprintf(output, "Tokens (%s, %s):\n", sanitize.TerminalText(report.Tokenizer), label)
	if report.ChunkLimitTokens > 0 {
		fmt.Fprintf(output, "  Diff chunk limit: %d tokens.\n", report.ChunkLimitTokens)
	}
	var input, cached, generated, reasoning, peak, reported, unknown int
	for i, chunk := range report.Chunks {
		fmt.Fprintf(output, "  Chunk %d: %d diff / %d prompt text tokens.\n", i+1, chunk.DiffTokens, chunk.PromptTextTokens)
		if len(chunk.Requests) == 0 {
			unknown++
		}
		for _, usage := range chunk.Requests {
			if usage == nil {
				unknown++
				continue
			}
			reported++
			input += usage.InputTokens
			cached += usage.CachedInputTokens
			generated += usage.OutputTokens
			reasoning += usage.ReasoningTokens
			peak = max(peak, usage.InputTokens)
		}
	}
	output.WriteString("  Prompt text excludes tool definitions, output schema, message framing, and later tool context.\n")
	if reported > 0 {
		fmt.Fprintf(output, "  Provider-reported peak input: %d tokens. Leave additional room for reasoning and output.\n", peak)
		fmt.Fprintf(output, "  Usage across %d reported requests: %d input (%d cached), %d output (%d reasoning included).\n", reported, input, cached, generated, reasoning)
	}
	if unknown > 0 {
		output.WriteString("  Provider usage is unavailable for some or all requests; reported totals and peak may be incomplete.\n")
	}
}

func filter(findings []model.Finding, blocking bool) []model.Finding {
	var result []model.Finding
	for _, finding := range findings {
		if finding.Blocking == blocking {
			result = append(result, finding)
		}
	}
	return result
}

func writeFindings(output *strings.Builder, heading string, findings []model.Finding) {
	output.WriteString(heading + "\n")
	if len(findings) == 0 {
		output.WriteString("  None.\n\n")
		return
	}
	for _, finding := range findings {
		location := ""
		if finding.Path != nil {
			location = " " + gitdiff.DisplayPath(*finding.Path)
			if finding.Line != nil {
				location += fmt.Sprintf(":%d", *finding.Line)
			}
		}
		fmt.Fprintf(output, "  %s [%s] %s%s\n    %s\n    Recommendation: %s\n",
			finding.ID, finding.Severity, sanitize.TerminalText(finding.Title), location,
			sanitize.TerminalText(finding.Explanation), sanitize.TerminalText(finding.Recommendation))
	}
	output.WriteString("\n")
}

func displayPaths(paths []string) []string {
	display := make([]string, len(paths))
	for index, path := range paths {
		display[index] = gitdiff.DisplayPath(path)
	}
	return display
}

func pointer(value *string) string {
	if value == nil {
		return "configured bot"
	}
	return *value
}
