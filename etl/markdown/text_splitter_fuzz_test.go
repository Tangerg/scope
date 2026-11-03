package markdown_test

import (
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/Tangerg/scope/etl"
	markdownsplitter "github.com/Tangerg/scope/etl/markdown"
)

const (
	fuzzMarkdownTokenLimitRange = 64
	fuzzMarkdownChunkLimit      = 256
)

func FuzzSplitterText(f *testing.F) {
	for _, seed := range []string{
		"",
		"# Heading\n\nparagraph",
		"- one\n- two\n- three",
		"```go\nfunc main() {}\n```",
		"| name | value |\n| --- | --- |\n| a | 1 |",
		"\x00\xffbroken utf-8",
	} {
		f.Add(seed, uint8(32))
	}

	f.Fuzz(func(t *testing.T, source string, encodedLimit uint8) {
		maxTokens := int(encodedLimit%fuzzMarkdownTokenLimitRange) + 1
		splitter, err := markdownsplitter.NewTextSplitter(markdownsplitter.TextSplitterConfig{
			Tokenizer:         runeTokenizer{},
			MaxTokensPerChunk: maxTokens,
			MaxChunks:         fuzzMarkdownChunkLimit,
		})
		if err != nil {
			t.Fatal(err)
		}

		chunks, err := splitter.SplitText(t.Context(), source)
		if errors.Is(err, markdownsplitter.ErrSemanticUnitTooLarge) ||
			errors.Is(err, etl.ErrChunkLimitExceeded) ||
			errors.Is(err, etl.ErrInvalidTextEncoding) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(chunks) > fuzzMarkdownChunkLimit {
			t.Fatalf("chunk count = %d, maximum is %d", len(chunks), fuzzMarkdownChunkLimit)
		}
		var combined strings.Builder
		for index, chunk := range chunks {
			if strings.TrimSpace(chunk) == "" || !utf8.ValidString(chunk) {
				t.Fatalf("chunk %d is empty or invalid UTF-8: %q", index, chunk)
			}
			if count := utf8.RuneCountInString(chunk); count > maxTokens {
				t.Fatalf("chunk %d has %d tokens, maximum is %d", index, count, maxTokens)
			}
			combined.WriteString(chunk)
		}

		want := nonMarkdownSpaceRunes(string([]rune(source)))
		got := nonMarkdownSpaceRunes(combined.String())
		if !runeSubsequence(want, got) {
			t.Fatalf("source content is not preserved: source %q, chunks %#v", source, chunks)
		}
	})
}

func nonMarkdownSpaceRunes(value string) []rune {
	return []rune(strings.Map(func(value rune) rune {
		if unicode.IsSpace(value) {
			return -1
		}
		return value
	}, value))
}

func runeSubsequence(want, within []rune) bool {
	matched := 0
	for _, value := range within {
		if matched < len(want) && value == want[matched] {
			matched++
		}
	}
	return matched == len(want)
}
