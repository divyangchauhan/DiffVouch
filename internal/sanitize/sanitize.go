package sanitize

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const placeholder = "[REDACTED BY DIFFVOUCH]"
const sensitiveKey = `(?:[A-Za-z0-9]+[_-])*(?:api[_-]?key|access[_-]?token|client[_-]?secret|password|secret[_-]?access[_-]?key|secret[_-]?key|private[_-]?key)\b`

var (
	assignment = regexp.MustCompile(`(?i)((?:"` + sensitiveKey + `"|'` + sensitiveKey + `'|` + sensitiveKey + `)\s*[:=]\s*)("(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|[^\s,;}]+)`)
	tokens     = []*regexp.Regexp{
		regexp.MustCompile(`\b(?:gh[pousr]|github_pat)_[A-Za-z0-9_]{20,}\b`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
		regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	}
	privateKeyBegin = regexp.MustCompile(`-----BEGIN (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----`)
	privateKeyEnd   = regexp.MustCompile(`-----END (?:RSA |EC |DSA |OPENSSH |ENCRYPTED )?PRIVATE KEY-----`)
	markdownEscaper = strings.NewReplacer(
		`\`, `\\`, "`", "\\`", `*`, `\*`, `_`, `\_`, `{`, `\{`, `}`, `\}`,
		`[`, `\[`, `]`, `\]`, `(`, `\(`, `)`, `\)`, `#`, `\#`, `+`, `\+`,
		`-`, `\-`, `.`, `\.`, `!`, `\!`, `|`, `\|`, `~`, `\~`,
		`&`, `&amp;`, `<`, `&lt;`, `>`, `&gt;`, `@`, `&#64;`,
	)
)

func Redact(value string) (string, int) {
	var output strings.Builder
	count := 0
	inPrivateKey := false
	for _, withEnding := range strings.SplitAfter(value, "\n") {
		ending := ""
		line := withEnding
		if strings.HasSuffix(line, "\n") {
			line = strings.TrimSuffix(line, "\n")
			ending = "\n"
		}
		prefix := ""
		content := line
		if content != "" && strings.Contains(" +-", content[:1]) {
			prefix, content = content[:1], content[1:]
		}
		if privateKeyBegin.MatchString(content) {
			inPrivateKey = true
			count++
		}
		if inPrivateKey {
			output.WriteString(prefix + placeholder + ending)
			if privateKeyEnd.MatchString(content) {
				inPrivateKey = false
			}
			continue
		}
		if match := assignment.FindStringSubmatchIndex(content); match != nil {
			insideString := match[0] > 0 && (content[match[0]-1] == '\'' || content[match[0]-1] == '"')
			if !insideString {
				valueStart, valueEnd := match[4], match[5]
				secretValue := content[valueStart:valueEnd]
				trimmed := strings.TrimSpace(secretValue)
				quote := ""
				quotedLiteral := len(trimmed) >= 2 && (trimmed[0] == '\'' || trimmed[0] == '"') && trimmed[len(trimmed)-1] == trimmed[0]
				unquotedLiteral := len(trimmed) >= 8 && !strings.ContainsAny(trimmed, " \t(){}[]") && strings.ContainsAny(trimmed, "0123456789_./+=:@!$%^&*-")
				if trimmed != "" && trimmed != placeholder && (quotedLiteral || unquotedLiteral) {
					if quotedLiteral {
						quote = trimmed[:1]
					}
					content = content[:valueStart] + quote + placeholder + quote + content[valueEnd:]
					count++
				}
			}
		}
		for _, pattern := range tokens {
			matches := pattern.FindAllStringIndex(content, -1)
			if len(matches) > 0 {
				count += len(matches)
				content = pattern.ReplaceAllString(content, placeholder)
			}
		}
		output.WriteString(prefix + content + ending)
	}
	return output.String(), count
}

// TerminalText escapes control and formatting characters in untrusted text
// while leaving ordinary Unicode readable.
func TerminalText(value string) string {
	var output strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf) {
			escaped := strconv.QuoteRuneToASCII(character)
			output.WriteString(escaped[1 : len(escaped)-1])
			continue
		}
		output.WriteRune(character)
	}
	return output.String()
}

// MarkdownText renders untrusted provider output as plain GitHub Markdown text.
// Newlines remain readable, while formatting syntax, mentions, HTML, and control
// characters are neutralized.
func MarkdownText(value string) string {
	var plain strings.Builder
	for _, character := range value {
		if character != '\n' && (unicode.IsControl(character) || unicode.In(character, unicode.Cf)) {
			escaped := strconv.QuoteRuneToASCII(character)
			plain.WriteString(escaped[1 : len(escaped)-1])
			continue
		}
		plain.WriteRune(character)
	}
	return markdownEscaper.Replace(plain.String())
}
