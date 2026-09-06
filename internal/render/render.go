package render

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/divyangchauhan/DiffVouch/internal/gitdiff"
	"github.com/divyangchauhan/DiffVouch/internal/model"
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
	fmt.Fprintf(&output, "DiffVouch review: %.1f/5 — %s\n\n%s\n\n", result.Rating.Overall, result.Rating.Label, result.Summary)
	fmt.Fprintf(&output, "Scope: %s · base %s (%s) · head %s\n\n", result.Scope.Mode, result.Scope.BaseRef, result.Scope.BaseSHA, result.Scope.HeadSHA)
	blocking := filter(result.Findings, true)
	nonBlocking := filter(result.Findings, false)
	writeFindings(&output, "Blocking issues", blocking)
	writeFindings(&output, "Non-blocking issues", nonBlocking)
	if len(result.PositiveObservations) > 0 {
		output.WriteString("Positive observations\n")
		for _, observation := range result.PositiveObservations {
			fmt.Fprintf(&output, "  - %s\n", observation)
		}
		output.WriteString("\n")
	}
	if len(result.NeedsVerification) > 0 {
		output.WriteString("Needs verification\n")
		for _, item := range result.NeedsVerification {
			fmt.Fprintf(&output, "  - %s\n", item)
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
		fmt.Fprintf(output, "  %s [%s] %s%s\n    %s\n    Recommendation: %s\n", finding.ID, finding.Severity, finding.Title, location, finding.Explanation, finding.Recommendation)
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
