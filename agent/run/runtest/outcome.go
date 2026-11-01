package runtest

import (
	"context"
	"crypto/rand"

	"github.com/Tangerg/scope/agent/run"
)

// Observe returns the committed logical outcome after local subtree cleanup.
// It is for conformance assertions which need failure and usage facts, rather
// than the successful output returned to an ordinary Runtime.Run caller.
func Observe(ctx context.Context, runtime *run.Runtime, p run.Program[run.Payload, run.Payload], in run.Payload) (run.Result[run.Payload], error) {
	h, err := runtime.Start(ctx, p, run.StartRequest[run.Payload]{ID: run.StartID{Scope: "conformance", Key: rand.Text()}, Input: in})
	if err != nil {
		return run.Result[run.Payload]{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = h.Cancel(context.WithoutCancel(ctx), "conformance caller canceled") })
	defer stop()
	settle := context.WithoutCancel(ctx)
	if err := h.Join(settle); err != nil {
		return run.Result[run.Payload]{}, err
	}
	return h.Await(settle)
}
