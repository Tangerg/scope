package managed_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/Tangerg/scope/a2a/managed"
	"github.com/Tangerg/scope/agent/run"
)

type cancellationReadStore struct {
	store         *run.MemoryStore
	mu            sync.Mutex
	afterSnapshot func()
	loadError     error
}

func (c *cancellationReadStore) Append(ctx context.Context, batch run.Batch) error {
	return c.store.Append(ctx, batch)
}
func (c *cancellationReadStore) Read(ctx context.Context, id run.ProcessID, after run.LSN) iter.Seq2[run.Entry, error] {
	return c.store.Read(ctx, id, after)
}
func (c *cancellationReadStore) Load(ctx context.Context, id run.ProcessID) (run.Log, bool, error) {
	c.mu.Lock()
	loadError := c.loadError
	callback := c.afterSnapshot
	c.afterSnapshot = nil
	c.mu.Unlock()
	if loadError != nil {
		return run.Log{}, false, loadError
	}
	log, found, err := c.store.Load(ctx, id)
	if callback != nil {
		callback()
	}
	return log, found, err
}
func (c *cancellationReadStore) afterNextSnapshot(callback func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.afterSnapshot = callback
}
func (c *cancellationReadStore) failReads(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loadError = err
}

func TestCancelTaskClassifiesTheCommittedOutcomeAfterAdmissionRaces(t *testing.T) {
	for _, scenario := range []struct {
		name          string
		complete      bool
		unreadable    bool
		wantErrorCode int
	}{
		{name: "completed", complete: true, wantErrorCode: -32002},
		{name: "writer-shutdown", wantErrorCode: -32603},
		{name: "completed-but-unreadable", complete: true, unreadable: true, wantErrorCode: -32603},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			store := &cancellationReadStore{store: run.NewMemoryStore()}
			runtime, err := run.New(ctx, run.Config{Build: "cancellation-race", Store: store})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := runtime.Close(context.WithoutCancel(ctx)); closeErr != nil {
					t.Error(closeErr)
				}
			})
			program := program(t, runtime.Compiler(), &dispatch{})
			server := server(t, runtime, program)
			initial := send(t, server, message("cancel-race", "ask"), false)
			root, err := run.ParseProcessID(initial.Metadata[managed.RootKey].(string))
			if err != nil {
				t.Fatal(err)
			}
			local, found := runtime.Process(root)
			if !found {
				t.Fatal("no local writer")
			}
			key, _ := run.ParseWaitKey("author")
			store.afterNextSnapshot(func() {
				if scenario.complete {
					if answerErr := local.Answer(ctx, key, "completed before cancel admission"); answerErr != nil {
						t.Error(answerErr)
						return
					}
					if _, awaitErr := local.Await(ctx); awaitErr != nil {
						t.Error(awaitErr)
					}
				} else if closeErr := runtime.Close(ctx); closeErr != nil {
					t.Error(closeErr)
				}
				if scenario.unreadable {
					store.failReads(errors.New("storage unavailable"))
				}
			})
			response := rpc(t, server, "alice", "CancelTask", map[string]any{"id": initial.ID})
			var protocolError struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			if err := jsonv2.Unmarshal(response.Error, &protocolError); err != nil {
				t.Fatal(err, string(response.Result))
			}
			if protocolError.Code != scenario.wantErrorCode {
				t.Fatalf("cancellation error=%+v, want code %d", protocolError, scenario.wantErrorCode)
			}
		})
	}
}
