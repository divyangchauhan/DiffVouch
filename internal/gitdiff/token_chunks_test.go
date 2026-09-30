package gitdiff

import (
	"errors"
	"strings"
	"testing"

	"github.com/divyangchauhan/DiffVouch/internal/tokens"
)

func TestTokenChunksPreservePatchAndRespectLimit(t *testing.T) {
	counter, err := tokens.New("gpt-5")
	if err != nil {
		t.Fatal(err)
	}
	header := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n"
	first := "@@ -1 +1 @@\n-old\n+" + strings.Repeat("こんにちは世界 ", 40) + "\n"
	second := "@@ -20 +20 @@\n-old2\n+" + strings.Repeat("你好世界 ", 40) + "\n"
	a, _ := counter.Count(header + first)
	b, _ := counter.Count(header + second)
	limit := max(a, b)
	chunks, err := ChunkPatchTokens(header+first+second, limit, counter.Count)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || chunks[0] != header+first || chunks[1] != header+second {
		t.Fatalf("hunks or repeated file headers lost: %#v", chunks)
	}
	for _, chunk := range chunks {
		count, err := counter.Count(chunk)
		if err != nil || count > limit {
			t.Fatalf("chunk exceeds token budget: %d > %d: %v", count, limit, err)
		}
	}
	// With enough space, retain one whole patch without duplicating its header.
	whole := header + first + second
	count, _ := counter.Count(whole)
	chunks, err = ChunkPatchTokens(whole, count, counter.Count)
	if err != nil || len(chunks) != 1 || chunks[0] != whole {
		t.Fatalf("whole-patch boundary failed: %#v %v", chunks, err)
	}
	// Whole file boundaries are preserved when packing multiple files as well.
	files := header + first + strings.ReplaceAll(header, "a.go", "b.go") + second
	chunks, err = ChunkPatchTokens(files, limit+5, counter.Count)
	if err != nil || len(chunks) != 2 || strings.Join(chunks, "") != files {
		t.Fatalf("file packing lost patch content: %#v %v", chunks, err)
	}
	if _, err := ChunkPatchTokens(header+first, a-1, counter.Count); err == nil || !strings.Contains(err.Error(), "tokens") {
		t.Fatalf("oversized hunk should fail explicitly: %v", err)
	}
}

func TestTokenChunkCounterFailureIsNotIgnored(t *testing.T) {
	want := errors.New("tokenization failed")
	_, err := ChunkPatchTokens("patch", 100, func(string) (int, error) { return 0, want })
	if !errors.Is(err, want) {
		t.Fatalf("lost tokenization error: %v", err)
	}
}
