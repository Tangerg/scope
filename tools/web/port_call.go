package web

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/tools/internal/toolresult"
)

// invokePort is the one call path of every web Tool. A request the model can
// correct returns to the model, a port failure keeps its cause, and the
// response must answer the request the port actually received.
func invokePort[Q any, R interface{ ValidateFor(*Q) error }](
	ctx context.Context,
	request interface{ Prepare() (*Q, error) },
	operation string,
	perform func(context.Context, *Q) (R, error),
) (R, error) {
	var zero R
	prepared, err := request.Prepare()
	if err != nil {
		return zero, toolresult.Correctable(err.Error(), err)
	}
	response, err := perform(ctx, prepared)
	if err != nil {
		return zero, fmt.Errorf("web.%s: %w", operation, err)
	}
	if validationErr := response.ValidateFor(prepared); validationErr != nil {
		return zero, fmt.Errorf("web: validate %s response: %w", operation, validationErr)
	}
	return response, nil
}
