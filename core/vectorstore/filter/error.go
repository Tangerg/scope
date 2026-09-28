package filter

import "fmt"

type SyntaxError struct {
	Position Position
	Token    string
	Message  string
}

func newSyntaxError(position Position, token, message string) *SyntaxError {
	return &SyntaxError{Position: position, Token: token, Message: message}
}

func (s *SyntaxError) Error() string {
	if s == nil {
		return "filter: syntax error"
	}
	if s.Token == "" {
		return fmt.Sprintf("filter: %s at %s", s.Message, s.Position)
	}
	return fmt.Sprintf("filter: %s at %s near %q", s.Message, s.Position, s.Token)
}
