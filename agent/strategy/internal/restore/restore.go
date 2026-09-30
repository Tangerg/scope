// Package restore owns the Restore protocol shared by built-in strategies:
// strict state decoding, Definition-consistency validation, and cooperative
// cancellation around that validation.
package restore

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent"
)

// Decode returns state of kind only after validate accepts it. Decoding errors
// wrap invalidState; validation errors are returned unchanged.
func Decode[S any](
	ctx context.Context,
	state agent.ExecutionState,
	kind string,
	invalidState error,
	validate func(context.Context, S) error,
) (S, error) {
	var zero S
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	decoded, err := state.Decode[S](kind)
	if err != nil {
		return zero, fmt.Errorf("%w: %w", invalidState, err)
	}
	if err := validate(ctx, decoded); err != nil {
		return zero, err
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	return decoded, nil
}
