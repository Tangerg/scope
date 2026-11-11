package managed_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/a2a"
	"github.com/Tangerg/scope/a2a/managed"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

const unconstrainedWaitKind = "test.unconstrained-wait"

type unconstrainedWaitDefinition struct {
	descriptor      run.Descriptor
	includeQuestion bool
}

func (u unconstrainedWaitDefinition) Descriptor() run.Descriptor { return u.descriptor }

func (u unconstrainedWaitDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] {
	return nil
}

func (u unconstrainedWaitDefinition) Start(run.Payload) (run.Execution, error) {
	return &unconstrainedWaitExecution{IncludeQuestion: u.includeQuestion}, nil
}

func (u unconstrainedWaitDefinition) Restore(_ context.Context, state run.ExecutionState) (run.Execution, error) {
	value, err := state.Decode[unconstrainedWaitExecution](unconstrainedWaitKind)
	return &value, err
}

type unconstrainedWaitExecution struct {
	Waiting         bool `json:"waiting"`
	IncludeQuestion bool `json:"include_question"`
}

func (u *unconstrainedWaitExecution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState(unconstrainedWaitKind, u)
}

func (u *unconstrainedWaitExecution) Step(_ context.Context, turn run.Turn) (run.Transition, error) {
	if !u.Waiting {
		u.Waiting = true
		key, err := run.ParseWaitKey("answer")
		if err != nil {
			return run.Transition{}, err
		}
		wait := run.Wait{Key: key}
		if u.IncludeQuestion {
			wait.Question, err = run.EncodePayload("Any JSON answer is permitted")
			if err != nil {
				return run.Transition{}, err
			}
		}
		return run.Yield(0, nil, wait)
	}
	signal, ok := turn.Messages[0].(run.Signal)
	if !ok {
		return run.Transition{}, run.ErrInvalidMessage
	}
	return run.Complete(1, signal.Payload)
}

func unconstrainedWaitProgram(t *testing.T, compiler *run.Compiler, includeQuestion bool) run.Program[string, run.Payload] {
	t.Helper()
	input, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	output, err := jsonschema.Parse([]byte(`true`))
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{Name: unconstrainedWaitKind, Description: "Wait for any JSON answer", InputSchema: input, OutputSchema: output})
	if err != nil {
		t.Fatal(err)
	}
	program, err := run.Bind[string, run.Payload](compiler, run.BindingConfig{Definition: unconstrainedWaitDefinition{descriptor: descriptor, includeQuestion: includeQuestion}, Revision: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func TestUnconstrainedWaitRoundTripsThroughA2A(t *testing.T) {
	for _, includeQuestion := range []bool{false, true} {
		for _, answer := range []string{`null`, `true`, `{"amount":9007199254740993}`, `"accepted"`} {
			name := "absent-question/"
			if includeQuestion {
				name = "present-question/"
			}
			t.Run(name+answer, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				runtime, err := run.New(ctx, run.Config{Build: unconstrainedWaitKind, Store: run.NewMemoryStore()})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if closeErr := runtime.Close(context.WithoutCancel(ctx)); closeErr != nil {
						t.Error(closeErr)
					}
				})
				program := unconstrainedWaitProgram(t, runtime.Compiler(), includeQuestion)
				server := httptest.NewUnstartedServer(nil)
				handler, err := managed.NewHTTPHandler(managed.Config{
					Runtime: runtime, Program: program.Erase(), Service: "wait-test",
					Authority: func(context.Context, string) (string, error) { return "caller", nil },
					Card: &sdk.AgentCard{Name: "wait-test", Description: "Unconstrained wait", DefaultOutputModes: []string{a2a.MediaTypePlainText},
						SupportedInterfaces: []*sdk.AgentInterface{sdk.NewAgentInterface("http://"+server.Listener.Addr().String()+"/rpc", sdk.TransportProtocolJSONRPC)}},
				})
				if err != nil {
					server.Close()
					t.Fatal(err)
				}
				server.Config.Handler = handler
				server.Start()
				t.Cleanup(server.Close)
				set, err := a2a.OpenToolSet(ctx, a2a.Endpoint{CardURL: server.URL, HTTPClient: server.Client()})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if closeErr := set.Close(); closeErr != nil {
						t.Error(closeErr)
					}
				})
				binding, err := tool.Bind(set.Tools()[0])
				if err != nil {
					t.Fatal(err)
				}
				request := tool.Request{Name: binding.Contract().Name(), Arguments: `{"message":"ask"}`}
				invocation, err := binding.Contract().Prepare(request, tool.Admission{})
				if err != nil {
					t.Fatal(err)
				}
				result, err := binding.Call(ctx, invocation)
				if err != nil || result.InputRequest == nil {
					t.Fatalf("valid optional wait did not become a continuation: result=%+v error=%v", result, err)
				}
				continuation, err := result.InputRequest.Continuation(json.RawMessage(answer))
				if err != nil {
					t.Fatal(err)
				}
				invocation, err = binding.Contract().Prepare(request, tool.Admission{Continuation: &continuation})
				if err != nil {
					t.Fatal(err)
				}
				result, err = binding.Call(ctx, invocation)
				if err != nil || result.InputRequest != nil {
					t.Fatalf("answer did not complete the wait: result=%+v error=%v", result, err)
				}
				want := answer
				if answer == `"accepted"` {
					want = "accepted"
				}
				if text, ok := result.Output.Text(); !ok || text != want {
					t.Fatalf("answer changed during transport: output=%+v, want %q", result.Output, want)
				}
			})
		}
	}
}

func TestNativeUnconstrainedWaitSurvivesRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	config := run.Config{Build: unconstrainedWaitKind, Store: run.NewMemoryStore(), CheckpointInterval: 1}
	first, err := run.New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := first.Close(context.WithoutCancel(ctx)); closeErr != nil {
			t.Error(closeErr)
		}
	})
	program := unconstrainedWaitProgram(t, first.Compiler(), true)
	handle, err := first.Start(ctx, program, run.StartRequest[string]{ID: run.StartID{Scope: "wait-test", Key: "native"}, Input: "ask"})
	if err != nil {
		t.Fatal(err)
	}
	checkpointed := false
	for entry, observeErr := range handle.Observe(ctx, 0) {
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		if checkpoint, ok := entry.Record.(run.Checkpoint); ok {
			for _, process := range checkpoint.Processes {
				checkpointed = checkpointed || len(process.Waits) == 1 && process.Waits[0].Pending && !process.Waits[0].Wait.AnswerSchema.Valid()
			}
		}
		if checkpointed {
			break
		}
	}
	if !checkpointed {
		t.Fatal("wait was not checkpointed")
	}
	if closeErr := first.Close(ctx); closeErr != nil {
		t.Fatal(closeErr)
	}
	second, err := run.New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := second.Close(context.WithoutCancel(ctx)); closeErr != nil {
			t.Error(closeErr)
		}
	})
	recovered, err := second.Recover(ctx, program, handle.ID())
	if err != nil {
		t.Fatal(err)
	}
	view, err := recovered.Inspect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	process, ok := view.Process(handle.ID())
	if !ok || len(process.Waits) != 1 || process.Waits[0].AnswerSchema.Valid() {
		t.Fatal("recovery changed optional wait semantics")
	}
	if answerErr := recovered.Answer(ctx, process.Waits[0].Key, "answered"); answerErr != nil {
		t.Fatal(answerErr)
	}
	result, err := recovered.Await(ctx)
	if err != nil {
		t.Fatal(err)
	}
	output, err := result.Output()
	if err != nil || string(output.JSON()) != `"answered"` {
		t.Fatalf("native answer failed: %s %v", output.JSON(), err)
	}
	payload, _ := run.EncodePayload("answered")
	matched, err := second.Answered(ctx, handle.ID(), process.Waits[0].Key, payload)
	if err != nil || !matched {
		t.Fatal("durable answer receipt missing", err)
	}
}
