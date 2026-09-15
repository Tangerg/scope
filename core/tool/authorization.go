package tool

import (
	"context"
	"errors"
	"fmt"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
)

// Authorization carries only the frozen model-visible contract and validated
// arguments, so policy code cannot bypass Contract validation or execute the invocation.
type Authorization struct {
	definition chat.ToolDefinition
	arguments  []byte
}

// Definition is detached because policy implementations may retain or annotate
// what they inspect without changing the executable contract.
func (a Authorization) Definition() chat.ToolDefinition {
	return a.definition.Clone()
}

// Arguments is detached for the same reason as Definition.
func (a Authorization) Arguments() []byte {
	return append([]byte(nil), a.arguments...)
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

// AuthorizerFunc adapts a plain function to [Authorizer], so a one-off policy
// does not require a named type.
type AuthorizerFunc func(context.Context, Authorization) (bool, error)

func (a AuthorizerFunc) Authorize(ctx context.Context, authorization Authorization) (bool, error) {
	return a(ctx, authorization)
}

// GuardConfig names both collaborators explicitly so neither can be defaulted.
// A guard silently constructed without an authorizer would report as protected
// while permitting everything.
type GuardConfig struct {
	Tool       Tool
	Authorizer Authorizer
}

// Guard keeps authorization at the universal Tool.Call boundary, which makes
// the same policy work for direct calls, registries, and managed runtimes.
// Refusal produces a generic FailureKindRejected result. A Tool that owns more
// specific public feedback uses NewFailure at its own invocation boundary.
type Guard struct {
	tool       Tool
	definition chat.ToolDefinition
	authorizer Authorizer
}

// NewGuard snapshots the wrapped tool's definition at construction, so the
// contract policy evaluates is the contract the model was shown. Resolving it
// per call would let a mutable inner tool widen its own arguments after
// approval. [Bind] compiles the frozen schema and [Contract.Prepare] validates
// invocations; construction validates only the definition's protocol shape.
func NewGuard(config GuardConfig) (Guard, error) {
	if lo.IsNil(config.Authorizer) {
		return Guard{}, fmt.Errorf("%w: authorizer is nil", ErrInvalidTool)
	}
	if lo.IsNil(config.Tool) {
		return Guard{}, fmt.Errorf("%w: authorization guard tool is nil", ErrInvalidTool)
	}
	definition := config.Tool.Definition()
	if err := definition.Validate(); err != nil {
		return Guard{}, fmt.Errorf("%w: authorization guard definition: %w", ErrInvalidTool, err)
	}
	return Guard{
		tool: config.Tool, definition: definition.Clone(), authorizer: config.Authorizer,
	}, nil
}

func (g Guard) Definition() chat.ToolDefinition {
	return g.definition.Clone()
}

func (g Guard) Call(ctx context.Context, invocation Invocation) (chat.ToolOutput, error) {
	if lo.IsNil(g.tool) || lo.IsNil(g.authorizer) {
		return chat.ToolOutput{}, fmt.Errorf("%w: authorization guard is zero", ErrInvalidTool)
	}
	if err := ctx.Err(); err != nil {
		return chat.ToolOutput{}, err
	}
	authorization := Authorization{
		definition: g.definition.Clone(), arguments: invocation.Arguments(),
	}
	allowed, err := g.authorizer.Authorize(ctx, authorization)
	if err != nil {
		return chat.ToolOutput{}, errors.Join(ctx.Err(), &AuthorizationError{name: g.definition.Name, cause: err})
	}
	if err := ctx.Err(); err != nil {
		return chat.ToolOutput{}, err
	}
	if !allowed {
		failure, err := NewFailure(FailureConfig{
			Kind:   FailureKindRejected,
			Output: chat.NewTextToolOutput(fmt.Sprintf("error: tool %q is not authorized", g.definition.Name)),
		})
		if err != nil {
			return chat.ToolOutput{}, err
		}
		return chat.ToolOutput{}, failure
	}
	return g.tool.Call(ctx, invocation)
}

func (g Guard) Unwrap() Tool { return g.tool }

var _ Tool = Guard{}
var _ WrappingTool = Guard{}
