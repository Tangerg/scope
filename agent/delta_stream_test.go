package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestDeltaStreamStopsAtCloseAndCountsRejectedIncrements(t *testing.T) {
	var nilStream *deltaStream
	if nilStream.emitter() != nil || nilStream.close() != 0 {
		t.Fatal("a stream without listeners exposed an emitter or drops")
	}

	var delivered []Delta
	bus := newObservationBus(nil, []DeltaListener{DeltaListenerFunc(func(_ context.Context, delta Delta) {
		delivered = append(delivered, delta)
	})}, 4)
	t.Cleanup(bus.close)
	stream := &deltaStream{
		observation: bus, context: t.Context(),
		processID: newProcessID(), effectID: newProcessID().effectID(1, 0),
		incarnationID: newTreeIncarnationID(), attemptID: newEffectAttemptID(),
	}
	emit := stream.emitter()
	emit(json.RawMessage(`{"n":1}`))
	emit(json.RawMessage(`{`))
	if dropped := stream.close(); dropped != 1 {
		t.Fatalf("dropped = %d, want the one invalid increment", dropped)
	}
	emit(json.RawMessage(`{"n":2}`))
	if dropped := stream.close(); dropped != 1 {
		t.Fatalf("an increment after close changed dropped to %d", dropped)
	}
	if err := bus.flushDeltas(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 || delivered[0].EffectSequence() != 1 {
		t.Fatalf("delivered %d Deltas, want only the increment before close", len(delivered))
	}
}
