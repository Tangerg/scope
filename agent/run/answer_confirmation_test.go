package run

import (
	"context"
	"errors"
	"testing"
)

func TestAnsweredRejectsInvalidQueryBeforeReadingStore(t *testing.T) {
	store := &publicObservationStore{store: NewMemoryStore(), load: func(context.Context, ProcessID) (Log, bool, error) {
		t.Error("invalid answer query reached storage")
		return Log{}, false, ErrInvalidLog
	}}
	runtime := kernelRuntime(t, Config{Store: store})
	process := controlValue(ParseProcessID("answer-query"))
	wait := kernelWait().Key
	payload := controlValue(EncodePayload(42))
	for _, sample := range []struct {
		name    string
		process ProcessID
		wait    WaitKey
		payload Payload
		cause   error
	}{
		{"invalid process", ProcessID{}, wait, payload, ErrInvalidIdentity},
		{"invalid wait", process, WaitKey{}, payload, ErrSignalRejected},
		{"invalid payload", process, wait, Payload{}, ErrInvalidPayload},
	} {
		t.Run(sample.name, func(t *testing.T) {
			answered, err := runtime.Answered(t.Context(), sample.process, sample.wait, sample.payload)
			if answered || !errors.Is(err, sample.cause) {
				t.Fatalf("invalid query: answered=%v error=%v", answered, err)
			}
		})
	}
}

func TestAnsweredConfirmsOnlyAddressedReceipts(t *testing.T) {
	for _, delivery := range []string{"none", "ordinary signal", "answer"} {
		t.Run(delivery, func(t *testing.T) {
			runtime := kernelRuntime(t, Config{})
			program := lifecycleEchoProgram(t, runtime, "test.answer_confirmation")
			ctx, cancel := context.WithTimeout(t.Context(), kernelTestTimeout)
			defer cancel()
			handle := controlValue(runtime.Start(ctx, program, StartRequest[int]{ID: hostStartID()}))
			waitForKernelStep(t, ctx, handle, 1)
			wait, payload := kernelWait().Key, controlValue(EncodePayload(42))
			if answered, err := runtime.Answered(ctx, handle.ID(), wait, payload); answered || err != nil {
				t.Fatalf("unanswered wait: answered=%v error=%v", answered, err)
			}
			switch delivery {
			case "ordinary signal":
				if _, err := handle.DeliverSignals(ctx, Signal{ID: controlValue(ParseSignalID("ordinary")), Payload: payload}); err != nil {
					t.Fatal(err)
				}
			case "answer":
				if err := handle.Answer(ctx, wait, 42); err != nil {
					t.Fatal(err)
				}
			}
			if delivery != "none" {
				if _, err := handle.Await(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if answered, err := runtime.Answered(ctx, handle.ID(), WaitKey{}, payload); answered || !errors.Is(err, ErrSignalRejected) {
				t.Fatalf("absent wait matched a receipt: answered=%v error=%v", answered, err)
			}
			if answered, err := runtime.Answered(ctx, handle.ID(), wait, payload); answered != (delivery == "answer") || err != nil {
				t.Fatalf("receipt confirmation: answered=%v error=%v", answered, err)
			}
			if delivery == "answer" {
				if answered, err := runtime.Answered(ctx, handle.ID(), wait, controlValue(EncodePayload(43))); answered || !errors.Is(err, ErrSignalConflict) {
					t.Fatalf("different answer: answered=%v error=%v", answered, err)
				}
			}
		})
	}
}
