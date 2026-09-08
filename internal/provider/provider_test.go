package provider

import (
	"encoding/json"
	"testing"

	"github.com/divyangchauhan/DiffVouch/internal/model"
)

func validReview() model.ProviderReview {
	return model.ProviderReview{
		Summary: "No issues.", Dimensions: model.Dimensions{Correctness: 5, Security: 5, Maintainability: 5, Testing: 5, Scope: 5},
		Findings: []model.Finding{}, PositiveObservations: []string{}, NeedsVerification: []string{},
	}
}

func TestStructuredReviewValidation(t *testing.T) {
	raw, _ := json.Marshal(validReview())
	if _, err := decodeReview(raw); err != nil {
		t.Fatal(err)
	}
	invalid := append(raw[:len(raw)-1], []byte(`,"unexpected":true}`)...)
	if _, err := decodeReview(invalid); err == nil {
		t.Fatal("unknown fields must fail")
	}
	if _, err := decodeReview(append(raw, []byte(" trailing")...)); err == nil {
		t.Fatal("trailing output must fail")
	}
	nullArrays := []byte(`{"summary":"x","dimensions":{"correctness":5,"security":5,"maintainability":5,"testing":5,"scope":5},"findings":null,"positiveObservations":[],"needsVerification":[]}`)
	if _, err := decodeReview(nullArrays); err == nil {
		t.Fatal("null arrays must fail")
	}
}

func TestAPIModeRequiresExplicitModel(t *testing.T) {
	if _, err := New(Options{Name: "codex", Transport: "api"}); err == nil {
		t.Fatal("OpenAI API should require a model")
	}
	if _, err := New(Options{Name: "claude", Transport: "api"}); err == nil {
		t.Fatal("Anthropic API should require a model")
	}
}

func TestSchemaRequiresNullableLocationFields(t *testing.T) {
	properties := schema()["properties"].(map[string]any)
	findings := properties["findings"].(map[string]any)
	items := findings["items"].(map[string]any)
	required := items["required"].([]string)
	for _, field := range []string{"path", "line", "side"} {
		found := false
		for _, value := range required {
			if value == field {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s is not required", field)
		}
	}
}
