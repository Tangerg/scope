package run

import (
	"context"
	"crypto/rand"
)

func observeOutcome(ctx context.Context, r *Runtime, p Program[Payload, Payload], input Payload) (Result[Payload], error) {
	h, err := r.startTree(ctx, ctx, p, input, StartID{Scope: "test", Key: rand.Text()})
	if err != nil {
		return Result[Payload]{}, err
	}
	settle := context.WithoutCancel(ctx)
	if err := h.Join(settle); err != nil {
		return Result[Payload]{}, err
	}
	return h.Await(settle)
}
func fixtureStart(snapshot TreeSnapshot) (TreeCheckpoint, error) {
	return newStartCheckpoint(snapshot, StartReceipt{ID: StartID{Scope: "test", Key: snapshot.RootID().String()}, RootID: snapshot.RootID(), Intent: ComputeDigest([]byte("fixture intent"))})
}
