package filter

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

type scanner struct {
	input  []rune
	offset int
	line   int
	column int
}

func newScanner(input string) *scanner {
	return &scanner{input: []rune(input), line: 1, column: 1}
}

func (s *scanner) next() (lexeme, error) {
	s.skipWhitespace()
	start := s.position()
	if s.done() {
		return lexeme{kind: tokenEOF, start: start, end: start}, nil
	}

	current := s.peek()
	if kind, ok := punctuationTokens[current]; ok {
		return s.single(kind, start), nil
	}
	switch {
	case current == '=':
		return s.scanPair('=', tokenEqual, start)
	case current == '!':
		return s.scanPair('=', tokenNotEqual, start)
	case current == '<':
		return s.scanOptionalPair('=', tokenLess, tokenLessEqual, start), nil
	case current == '>':
		return s.scanOptionalPair('=', tokenGreater, tokenGreaterEqual, start), nil
	case current == '\'':
		return s.scanString(start)
	case current == '-' || isASCIIDigit(current):
		return s.scanNumber(start)
	case unicode.IsLetter(current):
		return s.scanIdentifier(start), nil
	default:
		return lexeme{}, newSyntaxError(start, string(current), "invalid character")
	}
}

var punctuationTokens = map[rune]tokenKind{
	'(': tokenLeftParen,
	')': tokenRightParen,
	'[': tokenLeftBracket,
	']': tokenRightBracket,
	',': tokenComma,
}

func (s *scanner) scanPair(second rune, kind tokenKind, start Position) (lexeme, error) {
	first := s.take()
	if s.done() || s.peek() != second {
		return lexeme{}, newSyntaxError(start, string(first), fmt.Sprintf("expected %q", second))
	}
	s.take()
	return lexeme{kind: kind, literal: kind.string(), start: start, end: s.position()}, nil
}

func (s *scanner) scanOptionalPair(second rune, single, paired tokenKind, start Position) lexeme {
	s.take()
	kind := single
	if !s.done() && s.peek() == second {
		s.take()
		kind = paired
	}
	return lexeme{kind: kind, literal: kind.string(), start: start, end: s.position()}
}

func (s *scanner) scanString(start Position) (lexeme, error) {
	s.take()
	var value strings.Builder
	for !s.done() {
		current := s.take()
		if current == '\'' {
			return lexeme{
				kind: tokenString, literal: value.String(),
				start: start, end: s.position(),
			}, nil
		}
		if current != '\\' {
			value.WriteRune(current)
			continue
		}
		escapePosition := Position{Line: s.line, Column: s.column - 1}
		if s.done() {
			return lexeme{}, newSyntaxError(escapePosition, "\\", "unterminated string escape")
		}
		escaped := s.take()
		unescaped, ok := unescape(escaped)
		if !ok {
			return lexeme{}, newSyntaxError(
				escapePosition,
				"\\"+string(escaped),
				"invalid string escape",
			)
		}
		value.WriteRune(unescaped)
	}
	return lexeme{}, newSyntaxError(start, "", "unterminated string literal")
}

func unescape(r rune) (rune, bool) {
	switch r {
	case 'n':
		return '\n', true
	case 't':
		return '\t', true
	case 'r':
		return '\r', true
	case '\'', '"', '\\':
		return r, true
	default:
		return 0, false
	}
}

func (s *scanner) scanNumber(start Position) (lexeme, error) {
	var raw strings.Builder
	s.accept(&raw, '-')
	if !s.takeDigits(&raw) {
		return lexeme{}, newSyntaxError(start, "-", "expected digit after '-'")
	}
	if s.accept(&raw, '.') && !s.takeDigits(&raw) {
		return lexeme{}, newSyntaxError(start, raw.String(), "expected digit after decimal point")
	}
	if s.accept(&raw, 'e', 'E') {
		s.accept(&raw, '+', '-')
		if !s.takeDigits(&raw) {
			return lexeme{}, newSyntaxError(start, raw.String(), "expected digit in exponent")
		}
	}

	number, err := canonicalNumber(raw.String())
	if err != nil {
		return lexeme{}, newSyntaxError(start, raw.String(), err.Error())
	}
	return lexeme{kind: tokenNumber, literal: number, start: start, end: s.position()}, nil
}

func (s *scanner) accept(raw *strings.Builder, candidates ...rune) bool {
	if s.done() || !slices.Contains(candidates, s.peek()) {
		return false
	}
	raw.WriteRune(s.take())
	return true
}

func (s *scanner) takeDigits(raw *strings.Builder) bool {
	taken := false
	for !s.done() && isASCIIDigit(s.peek()) {
		raw.WriteRune(s.take())
		taken = true
	}
	return taken
}

func (s *scanner) scanIdentifier(start Position) lexeme {
	var raw strings.Builder
	for !s.done() {
		current := s.peek()
		if !unicode.IsLetter(current) && !unicode.IsDigit(current) && current != '_' {
			break
		}
		raw.WriteRune(s.take())
	}

	literal := raw.String()
	kind := keywordKind(literal)
	if kind == tokenIdent {
		return lexeme{kind: kind, literal: literal, start: start, end: s.position()}
	}
	if kind == tokenTrue || kind == tokenFalse {
		literal = strings.ToLower(literal)
	} else {
		literal = kind.string()
	}
	return lexeme{kind: kind, literal: literal, start: start, end: s.position()}
}

func keywordKind(literal string) tokenKind {
	switch strings.ToLower(literal) {
	case "true":
		return tokenTrue
	case "false":
		return tokenFalse
	case "and":
		return tokenAnd
	case "or":
		return tokenOr
	case "not":
		return tokenNot
	case "in":
		return tokenIn
	case "has":
		return tokenHas
	case "like":
		return tokenLike
	case "is":
		return tokenIs
	case string(LiteralNull):
		return tokenNull
	default:
		return tokenIdent
	}
}

func (s *scanner) single(kind tokenKind, start Position) lexeme {
	s.take()
	return lexeme{kind: kind, literal: kind.string(), start: start, end: s.position()}
}

func (s *scanner) skipWhitespace() {
	for !s.done() && unicode.IsSpace(s.peek()) {
		s.take()
	}
}

func (s *scanner) done() bool { return s.offset >= len(s.input) }

func (s *scanner) peek() rune { return s.input[s.offset] }

func (s *scanner) take() rune {
	current := s.input[s.offset]
	s.offset++
	if current == '\n' {
		s.line++
		s.column = 1
	} else {
		s.column++
	}
	return current
}

func (s *scanner) position() Position {
	return Position{Line: s.line, Column: s.column}
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }
