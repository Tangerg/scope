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
	query := Query{text: strings.TrimSpace(text)}
	if err := query.Validate(); err != nil {
		return Query{}, err
	}
	return query, nil
}

func (q Query) Validate() error {
	if !utf8.ValidString(q.text) {
		return fmt.Errorf("%w: text must be valid UTF-8", ErrInvalidQuery)
	}
	if q.text == "" || q.text != strings.TrimSpace(q.text) {
		return fmt.Errorf("%w: text must be non-empty without surrounding whitespace", ErrInvalidQuery)
	}
	return nil
}

func (q Query) Text() string { return q.text }
