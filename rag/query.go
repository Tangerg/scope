package rag

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	ErrInvalidQuery = errors.New("rag: invalid query")
	ErrNilRetriever = errors.New("rag: retriever must not be nil")
)

// Query is the text a retrieval stage acts on. It carries no side slots: a
// stage that needs more input, such as conversation history, declares it in
// its own input type instead of reading hidden state from the query.
type Query struct {
	text string
}

// NewQuery trims surrounding whitespace and requires valid UTF-8 text.
func NewQuery(text string) (Query, error) {
	trimmed := strings.TrimSpace(text)
	if !utf8.ValidString(trimmed) {
		return Query{}, fmt.Errorf("%w: text must be valid UTF-8", ErrInvalidQuery)
	}
	if trimmed == "" {
		return Query{}, fmt.Errorf("%w: text must not be blank", ErrInvalidQuery)
	}
	return Query{text: trimmed}, nil
}

// Validate rejects the zero Query. NewQuery is the only way to build any other
// value, so a non-zero Query already satisfies its text rule.
func (q Query) Validate() error {
	if q.text == "" {
		return fmt.Errorf("%w: zero query", ErrInvalidQuery)
	}
	return nil
}

func (q Query) Text() string { return q.text }
