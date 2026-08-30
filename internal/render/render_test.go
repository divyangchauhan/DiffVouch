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
