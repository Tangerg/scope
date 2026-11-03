package tool

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"
)

// Authorization carries only the frozen model-visible contract and validated
// arguments, so policy code cannot bypass Contract validation or execute the invocation.
type Authorization struct {
	invocation Invocation
}

// Definition is detached because policy implementations may retain or annotate
// what they inspect without changing the executable contract.
func (a Authorization) Definition() Definition {
	return a.invocation.Definition()
}

// Arguments is detached for the same reason as Definition.
func (a Authorization) Arguments() []byte {
	return a.invocation.Arguments()
}

// Authorizer is deliberately smaller than an application permission system:
// identity, consent, tenancy, and policy storage remain caller-owned context.
// A nil error establishes a decision: true permits execution, false refuses it.
// Any error means the decision could not be completed; its boolean is ignored.
// The Tool never executes after refusal or an authorization error.
// Guard seals policy errors in AuthorizationError; only cancellation of the
// execution context itself remains an errors.Is-visible control signal.
type Authorizer interface {
	Authorize(ctx context.Context, authorization Authorization) (bool, error)
}

type AuthorizerFunc func(context.Context, Authorization) (bool, error)

func (a AuthorizerFunc) Authorize(ctx context.Context, authorization Authorization) (bool, error) {
	return a(ctx, authorization)
}

type GuardConfig struct {
	Authorizer Authorizer
}

// Guard keeps authorization at the universal Tool.Call boundary, which makes
// the same policy work for direct calls, registries, and managed runtimes.
// Refusal produces a generic FailureKindRejected result. A Tool that owns more
// specific public feedback uses NewFailure at its own invocation boundary.
type Guard struct{ authorizer Authorizer }

func NewGuard(config GuardConfig) (Guard, error) {
	if lo.IsNil(config.Authorizer) {
		return Guard{}, fmt.Errorf("%w: authorizer is nil", ErrInvalidTool)
	}
	return Guard{authorizer: config.Authorizer}, nil
}

func (g Guard) Call(next Caller) Caller {
	return CallerFunc(func(ctx context.Context, invocation Invocation) (Result, error) {
		if lo.IsNil(g.authorizer) || lo.IsNil(next) {
			return Result{}, fmt.Errorf("%w: authorization middleware is unconfigured", ErrInvalidTool)
		}
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		name := invocation.Definition().Name
		allowed, err := g.authorizer.Authorize(ctx, Authorization{invocation: invocation})
		if err != nil {
			return Result{}, errors.Join(ctx.Err(), &AuthorizationError{name: name, cause: err})
		}
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if !allowed {
			failure, err := NewFailure(FailureConfig{Kind: FailureKindRejected, Output: NewTextOutput(fmt.Sprintf("tool %q is not authorized", name))})
			if err != nil {
				return Result{}, err
			}
			return Result{}, failure
		}
		return next.Call(ctx, invocation)
	})
}
