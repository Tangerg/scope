package agent

import (
	"context"
	"encoding/json"
	"math"
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

func TestDeltaStreamDoesNotReuseExhaustedSequence(t *testing.T) {
	var delivered []Delta
	bus := newObservationBus(nil, []DeltaListener{DeltaListenerFunc(func(_ context.Context, delta Delta) {
		delivered = append(delivered, delta)
	})}, 4)
	t.Cleanup(bus.close)
	stream := &deltaStream{
		observation: bus, context: t.Context(),
		processID: newProcessID(), effectID: newProcessID().effectID(1, 0),
		incarnationID: newTreeIncarnationID(), attemptID: newEffectAttemptID(),
		sequence: math.MaxUint64 - 1,
	}
	for range 3 {
		stream.emit(json.RawMessage(`{"text":"increment"}`))
	}
	dropped := stream.close()
	if err := bus.flushDeltas(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 || delivered[0].EffectSequence() != math.MaxUint64 || dropped != 2 {
		t.Fatalf("delivered = %+v, dropped = %d; want only the final sequence and two drops", delivered, dropped)
	}
}

func TestDeltaStreamDroppedCountSaturates(t *testing.T) {
	for _, test := range []struct {
		name     string
		sequence uint64
		payload  json.RawMessage
	}{
		{name: "invalid payload", payload: json.RawMessage(`{`)},
		{name: "exhausted sequence", sequence: math.MaxUint64, payload: json.RawMessage(`{}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			bus := newObservationBus(nil, nil, 0)
			t.Cleanup(bus.close)
			stream := &deltaStream{
				observation: bus, context: t.Context(),
				processID: newProcessID(), effectID: newProcessID().effectID(1, 0),
				incarnationID: newTreeIncarnationID(), attemptID: newEffectAttemptID(),
				sequence: test.sequence, dropped: math.MaxUint64 - 1,
			}
			for range 2 {
				stream.emit(test.payload)
			}
			if dropped := stream.close(); dropped != math.MaxUint64 {
				t.Fatalf("dropped = %d, want saturation at %d", dropped, uint64(math.MaxUint64))
			}
		})
	}
}
