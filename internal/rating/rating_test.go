package rating

import (
	"testing"

	"github.com/divyangchauhan/DiffVouch/internal/model"
)

func TestHighFindingCapsRatingAndBecomesBlocking(t *testing.T) {
	review := model.ProviderReview{Dimensions: model.Dimensions{Correctness: 5, Security: 5, Maintainability: 5, Testing: 5, Scope: 5}, Findings: []model.Finding{{Severity: model.High}}}
	review.Findings = NormalizeFindings(review.Findings)
	rating := Calculate(review, map[string]int{"correctness": 35, "security": 20, "maintainability": 20, "testing": 15, "scope": 10})
	if rating.Overall != 2.5 || !review.Findings[0].Blocking || review.Findings[0].ID != "DV-001" {
		t.Fatalf("normalization mismatch: %#v %#v", rating, review.Findings)
	}
}

func TestQualityGate(t *testing.T) {
	threshold := 4.0
	gate := Gate(model.Rating{Overall: 3.5}, []model.Finding{{ID: "DV-001", Severity: model.High}}, &threshold, "high")
	if gate.Passed || len(gate.Reasons) != 2 {
		t.Fatalf("gate should fail twice: %#v", gate)
	}
}

func TestCriticalRatingLabel(t *testing.T) {
	review := model.ProviderReview{Dimensions: model.Dimensions{Correctness: 1, Security: 1, Maintainability: 1, Testing: 1, Scope: 1}}
	result := Calculate(review, map[string]int{"correctness": 35, "security": 20, "maintainability": 20, "testing": 15, "scope": 10})
	if result.Overall != 1 || result.Label != "Critical risk" {
		t.Fatalf("unexpected critical rating: %#v", result)
	}
}
