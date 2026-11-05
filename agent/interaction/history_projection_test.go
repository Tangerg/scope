package interaction

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func toolRoundHistory(t *testing.T, rounds int) (run.Program[Input, Output], run.Log) {
	t.Helper()
	store := run.NewMemoryStore()
	runtime := conformancetest.Runtime(t, run.Config{Store: store})
	echo := scriptTool{name: "echo", invoke: func(_ context.Context, input tool.Invocation) (tool.Result, error) {
		return tool.Result{Output: tool.NewTextOutput(requestedCall(input))}, nil
	}}
	models := 0
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		models++
		if models <= rounds {
			return callResponse(requested("echo", fmt.Sprintf("call-%d", models))), nil
		}
		return finalResponse("done"), nil
	})
	program := compileTest(t, runtime, Config{Model: CallModel(model), Tools: toolSetTest(t, echo)})
	handle := startInteraction(t, runtime, program)
	awaitOutput(t, handle)
	return program, completedLog(t, store, handle.ID())
}

func roundDigests(t *testing.T, rounds []RoundResults) []string {
	t.Helper()
	digests := make([]string, len(rounds))
	for index, round := range rounds {
		digests[index] = round.Digest().String()
	}
	return digests
}

// Each read must reach the same verdict as a fresh projection of that history,
// whatever the projection remembers from earlier reads.
func TestHistoryProjectionRereadsMatchFreshProjection(t *testing.T) {
	program, log := toolRoundHistory(t, 3)
	projection := mustValue(NewHistoryProjection(program))
	var prefix run.Log
	for end := range log.Entries {
		prefix = run.Log{Root: log.Root, Entries: log.Entries[:end+1]}
		if prefix.Validate() != nil {
			continue
		}
		fresh, freshErr := mustValue(NewHistoryProjection(program)).SettledResults(prefix)
		reread, rereadErr := projection.SettledResults(prefix)
		if (freshErr == nil) != (rereadErr == nil) || !reflect.DeepEqual(roundDigests(t, fresh), roundDigests(t, reread)) {
			t.Fatalf("prefix %d: reread %v/%v, fresh %v/%v", end+1, roundDigests(t, reread), rereadErr, roundDigests(t, fresh), freshErr)
		}
	}
	if got := mustValue(projection.SettledResults(log)); len(got) != 3 {
		t.Fatalf("complete history projected %d rounds", len(got))
	}

	var wg sync.WaitGroup
	want := roundDigests(t, mustValue(projection.SettledResults(log)))
	for range 8 {
		wg.Go(func() {
			got, err := projection.SettledResults(log)
			if err != nil || !reflect.DeepEqual(roundDigests(t, got), want) {
				t.Errorf("concurrent read = %v, %v", got, err)
			}
		})
	}
	wg.Wait()
}

// A remembered decoding is evidence only for the exact canonical payload that
// produced it; the same EffectID with other bytes must be decoded again.
func TestHistoryProjectionReusesDecodingOnlyForIdenticalPayloads(t *testing.T) {
	program, log := toolRoundHistory(t, 1)
	projection := mustValue(NewHistoryProjection(program))
	want := roundDigests(t, mustValue(projection.SettledResults(log)))
	unrelated := mustValue(run.ParsePayload([]byte(`{"unrelated":true}`)))
	projection.mu.Lock()
	stale := make(map[run.EffectID]dispatchFact, len(projection.dispatches))
	for id, fact := range projection.dispatches {
		fact.payload = unrelated
		fact.modelCallSequence += 100
		stale[id] = fact
	}
	projection.dispatches = stale
	projection.mu.Unlock()
	if got := roundDigests(t, mustValue(projection.SettledResults(log))); !reflect.DeepEqual(got, want) {
		t.Fatalf("decoding for another payload was reused: %v, want %v", got, want)
	}
}
