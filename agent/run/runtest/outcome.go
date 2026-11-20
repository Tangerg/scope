package runtest

import (
	"context"
	"crypto/rand"
	"errors"

	"github.com/Tangerg/scope/agent/run"
)

// Observe exposes terminal failure and usage facts for conformance assertions.
// Cancellation gets a bounded interval to observe local draining; an Unknown
// external outcome stays in the log without delaying the canceled Result.
func Observe(ctx context.Context, runtime *run.Runtime, program run.Program[run.Payload, run.Payload], input run.Payload) (run.Result[run.Payload], error) {
	handle, err := runtime.Start(ctx, program, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "conformance", Key: rand.Text()}, Input: input})
	if err != nil {
		return run.Result[run.Payload]{}, err
	}
	result, awaitErr := handle.Await(ctx)
	if awaitErr == nil || ctx.Err() == nil {
		return result, awaitErr
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), conformanceTimeout)
	defer cancel()
	cancelErr := handle.Cancel(cleanup, "conformance caller canceled")
	result, cleanupErr := handle.Await(cleanup)
	if cleanupErr != nil {
		return run.Result[run.Payload]{}, errors.Join(awaitErr, cancelErr, cleanupErr)
	}
	if cancelErr != nil && !errors.Is(cancelErr, run.ErrClosed) && !errors.Is(cancelErr, run.ErrProcessFinished) {
		return result, errors.Join(awaitErr, cancelErr)
	}
	return result, nil
}
