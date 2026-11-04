package safeguard

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrUnsafeContent           = errors.New("safeguard: unsafe content")
	ErrInvalidMiddlewareConfig = errors.New("safeguard: invalid middleware config")
	ErrInvalidSubstringConfig  = errors.New("safeguard: invalid substring matcher config")
)

// Scope selects which directions a Middleware screens. Both is the zero value.
type Scope string

const (
	ScopeBoth Scope = ""
	// ScopeInput screens system and user text before provider I/O.
	ScopeInput Scope = Scope(DirectionInput)
	// ScopeOutput screens assistant text before it reaches the caller.
	ScopeOutput Scope = Scope(DirectionOutput)
)

func (s Scope) Valid() bool {
	switch s {
	case ScopeInput, ScopeOutput, ScopeBoth:
		return true
	default:
		return false
	}
}

func (s Scope) inspects(direction Direction) bool {
	return direction.Valid() && (s == ScopeBoth || s == Scope(direction))
}

// Direction identifies the single content flow rejected by a policy decision.
type Direction string

const (
	DirectionInput  Direction = "input"
	DirectionOutput Direction = "output"
)

func (d Direction) Valid() bool {
	return d == DirectionInput || d == DirectionOutput
}

// Match is a Matcher's decision for one text projection. Term should be empty
// when policy details must not be disclosed.
type Match struct {
	Term  string
	Found bool
}

// Matcher screens a text projection, possibly through a remote policy service.
type Matcher interface {
	// Match evaluates one provider-neutral text projection. Found=false means the
	// text passed; a returned error means no policy decision was reached. Remote
	// implementations must honor ctx and preserve context errors.
	Match(ctx context.Context, text string) (Match, error)
}

type MatcherFunc func(ctx context.Context, text string) (Match, error)

func (m MatcherFunc) Match(ctx context.Context, text string) (Match, error) {
	return m(ctx, text)
}

type Block struct {
	Direction Direction
	Term      string
}

// UnsafeError is a policy rejection. It unwraps to ErrUnsafeContent.
type UnsafeError struct {
	Block Block
}

func (u *UnsafeError) Error() string {
	if u == nil {
		return ErrUnsafeContent.Error()
	}
	if u.Block.Term == "" {
		return fmt.Sprintf("%s: %s blocked", ErrUnsafeContent, u.Block.Direction)
	}
	return fmt.Sprintf("%s: %s matched %q", ErrUnsafeContent, u.Block.Direction, u.Block.Term)
}

func (u *UnsafeError) Unwrap() error {
	return ErrUnsafeContent
}
