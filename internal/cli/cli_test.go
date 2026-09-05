package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveReviewPromptDefaultsToEmbeddedGuidance(t *testing.T) {
	value, err := resolveReviewPrompt(strings.NewReader("unused"), "", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if value != "" {
		t.Fatalf("expected an empty override, got %q", value)
	}
}

func TestResolveReviewPromptSupportsInlineFileAndStdin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.md")
	if err := os.WriteFile(path, []byte("  file guidance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name               string
		reader             *strings.Reader
		inline, path       string
		inlineSet, fileSet bool
		expected           string
	}{
		{name: "inline", reader: strings.NewReader(""), inline: "  inline guidance ", inlineSet: true, expected: "inline guidance"},
		{name: "file", reader: strings.NewReader(""), path: path, fileSet: true, expected: "file guidance"},
		{name: "stdin", reader: strings.NewReader(" stdin guidance\n"), path: "-", fileSet: true, expected: "stdin guidance"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, err := resolveReviewPrompt(test.reader, test.inline, test.path, test.inlineSet, test.fileSet)
			if err != nil {
				t.Fatal(err)
			}
			if value != test.expected {
				t.Fatalf("got %q, want %q", value, test.expected)
			}
		})
	}
}

func TestResolveReviewPromptRejectsInvalidOverrides(t *testing.T) {
	tests := []struct {
		name               string
		reader             *strings.Reader
		inline, path       string
		inlineSet, fileSet bool
		message            string
	}{
		{name: "both", reader: strings.NewReader(""), inline: "one", path: "two", inlineSet: true, fileSet: true, message: "mutually exclusive"},
		{name: "empty inline", reader: strings.NewReader(""), inline: " \n", inlineSet: true, message: "cannot be empty"},
		{name: "empty stdin", reader: strings.NewReader(" \n"), path: "-", fileSet: true, message: "cannot be empty"},
		{name: "too large", reader: strings.NewReader(""), inline: strings.Repeat("x", maxReviewPromptBytes+1), inlineSet: true, message: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveReviewPrompt(test.reader, test.inline, test.path, test.inlineSet, test.fileSet)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("got %v, want error containing %q", err, test.message)
			}
		})
	}
}
