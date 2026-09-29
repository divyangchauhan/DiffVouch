package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/divyangchauhan/DiffVouch/internal/model"
)

func TestCLIReviewHasRepositoryAndShellAccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake provider uses a POSIX shell")
	}
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			repo, bin := t.TempDir(), t.TempDir()
			capture := filepath.Join(bin, "args")
			response, _ := json.Marshal(validReview())
			for path, data := range map[string][]byte{
				filepath.Join(repo, "context.txt"):  []byte("repository context"),
				filepath.Join(bin, "response.json"): response,
				filepath.Join(bin, name): []byte(`#!/bin/sh
if [ "$1" = login ] || [ "$1" = auth ]; then exit 0; fi
cat context.txt > "$DIFFVOUCH_TEST_CONTEXT" || exit 1
printf '%s\n' "$@" > "$DIFFVOUCH_TEST_ARGS"
cat > "$DIFFVOUCH_TEST_PROMPT"
output=
while [ "$#" -gt 0 ]; do
  if [ "$1" = --output-last-message ]; then shift; output=$1; fi
  shift
done
if [ -n "$output" ]; then
  cat "$DIFFVOUCH_TEST_RESPONSE" > "$output"
else
  printf '{"structured_output":'
  cat "$DIFFVOUCH_TEST_RESPONSE"
  printf '}\n'
fi
`),
			} {
				if err := os.WriteFile(path, data, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("DIFFVOUCH_TEST_ARGS", capture)
			t.Setenv("DIFFVOUCH_TEST_CONTEXT", filepath.Join(bin, "context"))
			t.Setenv("DIFFVOUCH_TEST_PROMPT", filepath.Join(bin, "prompt"))
			t.Setenv("DIFFVOUCH_TEST_RESPONSE", filepath.Join(bin, "response.json"))
			adapter, err := New(Options{Name: name, Transport: "cli", Root: repo, Model: "test-model", Effort: "high"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Review(Prompt{System: "Review with tools.", User: "test patch"})
			if err != nil || result.Summary != "No issues." {
				t.Fatalf("review failed: %#v, %v", result, err)
			}
			for file, expected := range map[string]string{"context": "repository context", "prompt": "test patch"} {
				data, err := os.ReadFile(filepath.Join(bin, file))
				if err != nil || string(data) != expected {
					t.Fatalf("%s: got %q, %v", file, data, err)
				}
			}
			args, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			required := []string{"--model\ntest-model\n"}
			forbidden := []string{"--sandbox\nread-only\n", "--disable\nshell_tool\n", "--tools\n\n"}
			if name == "codex" {
				required = append(required, "--dangerously-bypass-approvals-and-sandbox\n", "--enable\nshell_tool\n", "--output-schema\n", "model_reasoning_effort=\"high\"\n")
			} else {
				required = append(required, "--dangerously-skip-permissions\n", "--tools\nBash,Read,Glob,Grep\n", "--effort\nhigh\n")
			}
			for _, value := range required {
				if !strings.Contains(string(args), value) {
					t.Errorf("missing arguments %q in %s", value, args)
				}
			}
			for _, value := range forbidden {
				if strings.Contains(string(args), value) {
					t.Errorf("restricted arguments %q in %s", value, args)
				}
			}
		})
	}
}

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
