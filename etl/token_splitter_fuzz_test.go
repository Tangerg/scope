package etl_test

import (
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/Tangerg/scope/etl"
)

const (
	fuzzTokenLimitRange = 32
	fuzzChunkLimit      = 256
)

func FuzzTokenSplitterText(f *testing.F) {
	for _, seed := range []string{
		"",
		"plain text",
		"first sentence. 第二句。\nthird line",
		"\x00\xffbroken utf-8",
	} {
		f.Add(seed, uint8(8), false)
	}

	f.Fuzz(func(t *testing.T, source string, encodedLimit uint8, preserveNewlines bool) {
		maxTokens := int(encodedLimit%fuzzTokenLimitRange) + 1
		splitter, err := etl.NewTokenSplitter(etl.TokenSplitterConfig{
			Tokenizer:         runeTokenizer{},
			MaxTokensPerChunk: maxTokens,
			MinTokensPerChunk: 1,
			MaxChunks:         fuzzChunkLimit,
			PreserveNewlines:  preserveNewlines,
		})
		if err != nil {
			t.Fatal(err)
		}

		chunks, err := splitter.SplitText(t.Context(), source)
		if errors.Is(err, etl.ErrChunkLimitExceeded) ||
			errors.Is(err, etl.ErrInvalidTextEncoding) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(chunks) > fuzzChunkLimit {
			t.Fatalf("chunk count = %d, maximum is %d", len(chunks), fuzzChunkLimit)
		}
		for index, chunk := range chunks {
			if chunk == "" || !utf8.ValidString(chunk) {
				t.Fatalf("chunk %d is empty or invalid UTF-8: %q", index, chunk)
			}
			if count := utf8.RuneCountInString(chunk); count > maxTokens {
				t.Fatalf("chunk %d has %d tokens, maximum is %d", index, count, maxTokens)
			}
		}

		want := nonSpaceRunes(string([]rune(source)))
		got := nonSpaceRunes(strings.Join(chunks, ""))
		if got != want {
			t.Fatalf("non-space content changed: got %q, want %q", got, want)
		}
	})
}

func nonSpaceRunes(value string) string {
	return strings.Map(func(value rune) rune {
		if unicode.IsSpace(value) {
			return -1
		}
		return value
	}, value)
}
