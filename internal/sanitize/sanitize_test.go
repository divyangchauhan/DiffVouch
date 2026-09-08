package sanitize

import (
	"strings"
	"testing"
)

func TestRedactCommonSecretsAndPreserveDiffShape(t *testing.T) {
	aws := "AKIA" + strings.Repeat("A", 16)
	input := "+DATABASE_PASSWORD=\"p@ss! word#punctuation\"\n+TOKEN=" + aws + "\n+-----BEGIN ENCRYPTED PRIVATE KEY-----\n+ciphertext\n+-----END ENCRYPTED PRIVATE KEY-----\n"
	output, count := Redact(input)
	if strings.Contains(output, "p@ss!") || strings.Contains(output, aws) || strings.Contains(output, "ciphertext") {
		t.Fatalf("secret leaked: %s", output)
	}
	if count != 3 || strings.Count(output, "\n") != strings.Count(input, "\n") {
		t.Fatalf("count/shape mismatch: count=%d output=%q", count, output)
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if !strings.HasPrefix(line, "+") {
			t.Fatalf("diff prefix lost: %q", line)
		}
	}
}

func TestRedactDoesNotRewriteSecretReferenceCode(t *testing.T) {
	inputs := []string{
		`client := Client{privateKey: string(raw), HTTP: http.DefaultClient}`,
		`storeSecret("api-key:"+provider, secret)`,
		`privateKey = ref`, `inPrivateKey = true`, `password = 123`, `apiKey = loadKey()`,
	}
	for _, input := range inputs {
		output, count := Redact(input)
		if output != input || count != 0 {
			t.Fatalf("ordinary source was changed: %q -> %q (%d)", input, output, count)
		}
	}
}

func TestRedactQuotedConfigurationKey(t *testing.T) {
	input := `+{"password":"json-secret"}`
	output, count := Redact(input)
	if strings.Contains(output, "json-secret") || count != 1 {
		t.Fatalf("quoted-key secret leaked: count=%d output=%s", count, output)
	}
}

func TestTerminalTextEscapesControlAndFormattingCharacters(t *testing.T) {
	input := "summary\n\x1b]52;c;payload\a\u202eforged"
	output := TerminalText(input)
	if strings.ContainsAny(output, "\n\x1b\a\u202e") {
		t.Fatalf("terminal control character remained: %q", output)
	}
	for _, escaped := range []string{`\n`, `\x1b`, `\a`, `\u202e`} {
		if !strings.Contains(output, escaped) {
			t.Fatalf("missing escape %q in %q", escaped, output)
		}
	}
}

func TestMarkdownTextNeutralizesFormattingHTMLAndMentions(t *testing.T) {
	input := "# forged [link](https://example.invalid) <details> @octocat\n**bold**\x1b[2J"
	output := MarkdownText(input)
	for _, escaped := range []string{`\# forged`, `\[link\]`, `&lt;details&gt;`, `&#64;octocat`, `\*\*bold\*\*`, `\\x1b`} {
		if !strings.Contains(output, escaped) {
			t.Fatalf("missing escaped Markdown %q in %q", escaped, output)
		}
	}
	if strings.Contains(output, "<") || strings.Contains(output, "@octocat") || strings.ContainsRune(output, '\x1b') {
		t.Fatalf("active HTML, mention, or control character remained in %q", output)
	}
}
