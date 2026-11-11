package interaction

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type delegateValue struct {
	Value int `json:"value"`
}

func compiledDelegate(t testing.TB, compiler *run.Compiler, name string, transform func(context.Context, delegateValue) (delegateValue, error)) Delegate {
	t.Helper()
	stage, err := workflow.Transform("apply", transform)
	if err != nil {
		t.Fatal(err)
	}
	program, err := workflow.Compile[delegateValue, delegateValue](compiler, workflow.Config{Name: "test." + name, Description: "Run a typed delegate operation.", Revision: "delegate-operation", Stages: []workflow.Stage{stage}})
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := NewDelegate(DelegateConfig{Name: name, Description: "Apply the declared operation.", Child: run.ChildBinding{Program: program.Erase()}})
	if err != nil {
		t.Fatal(err)
	}
	return delegate
}

func TestCompileRejectsIncompleteOrContradictoryAuthority(t *testing.T) {
	compiler := conformancetest.Compiler(t)
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) { return finalResponse("unused"), nil })
	streamer := chat.StreamerFunc(func(context.Context, *chat.Request) iter.Seq2[*chat.ResponseDelta, error] { return nil })
	delegate := compiledDelegate(t, compiler, "worker", func(_ context.Context, value delegateValue) (delegateValue, error) { return value, nil })
	for _, row := range []struct {
		name   string
		change func(*Config)
	}{
		{"missing revision", func(config *Config) { config.Revision = "" }},
		{"invalid name", func(config *Config) { config.Name = "Invalid Name" }},
		{"empty description", func(config *Config) { config.Description = "" }},
		{"missing model", func(config *Config) { config.Model = nil }},
		{"typed nil model", func(config *Config) { config.Model = CallModel(chat.ModelFunc(nil)) }},
		{"typed nil streamer", func(config *Config) { config.Model = StreamModel(StreamModelConfig{Streamer: chat.StreamerFunc(nil)}) }},
		{"typed nil processor", func(config *Config) { config.ContextProcessor = contextProcessorFunc(nil) }},
		{"negative response bound", func(config *Config) { config.MaxResponseBytes = -1 }},
		{"oversized response bound", func(config *Config) { config.MaxResponseBytes = run.MaxPayloadBytes + 1 }},
		{"response bound below any response", func(config *Config) { config.MaxResponseBytes = 1 }},
		{"zero model admission", func(config *Config) { config.MaxModelCalls = run.NewQuota(0) }},
		{"negative concurrency", func(config *Config) { config.MaxConcurrentToolCalls = -1 }},
		{"unbound scheduling", func(config *Config) { config.MaxConcurrentToolCalls = 2 }},
		{"unbound capability", func(config *Config) {
			config.RequiredToolCapabilities = mustValue(run.NewCapabilitySet(mustValue(run.ParseCapability("test.write"))))
		}},
		{"negative stream bound", func(config *Config) {
			config.Model = StreamModel(StreamModelConfig{Streamer: streamer, MaxDeltaBytes: -1})
		}},
		{"invalid delegate", func(config *Config) { config.Delegates = []Delegate{{}} }},
		{"duplicate delegate", func(config *Config) { config.Delegates = []Delegate{delegate, delegate} }},
		{"tool and delegate collision", func(config *Config) {
			config.Delegates = []Delegate{delegate}
			config.Tools = toolSetTest(t, scriptTool{name: "worker"})
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			config := Config{Name: "test.valid", Description: "Validate compilation boundaries.", Revision: "model", Model: CallModel(model)}
			row.change(&config)
			program, err := Compile(compiler, config)
			if err == nil || program.Valid() {
				t.Fatalf("invalid configuration admitted: %+v", program)
			}
		})
	}
	if program, err := Compile(nil, Config{Name: "test.valid", Description: "Require compiler identity.", Revision: "model", Model: CallModel(model)}); err == nil || program.Valid() {
		t.Fatal("compilation succeeded without build authority")
	}
	definition := mustValue(newDefinition(Config{Name: "test.context", Description: "Require the declared processor.", ContextProcessor: contextProcessorFunc(nil)}))
	if _, err := newDispatcher(definition, Config{Model: CallModel(model)}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal("processor capability did not match compiled execution", err)
	}
}

type capabilityPanicTool struct {
	mode string
}

func (c capabilityPanicTool) Definition() tool.Definition {
	return tool.Definition{Name: "panic", Description: "Expose an invalid capability declaration.", InputSchema: json.RawMessage(objectToolSchema)}
}

func (c capabilityPanicTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	return tool.Result{}, errors.New("must not run")
}

func (c capabilityPanicTool) ReturnsDirectResult() bool {
	if c.mode == "direct" {
		panic("invalid direct-result declaration")
	}
	return false
}

func (c capabilityPanicTool) ConcurrencyPolicy() func(tool.Invocation) (string, bool) {
	if c.mode == "concurrent" {
		panic("invalid concurrency declaration")
	}
	return nil
}

func (c capabilityPanicTool) InputValidator() func([]byte) error { return nil }

func TestToolSetRejectsMissingBindingsAndCapabilityDefects(t *testing.T) {
	executable := scriptTool{name: "echo"}
	for _, config := range []ToolSetConfig{
		{}, {Revision: "empty"}, {Tools: []tool.Tool{executable}},
		{Revision: "nil", Tools: []tool.Tool{nil}},
		{Revision: "nil deferred", DeferredTools: []tool.Tool{nil}},
		{Revision: "unrepresentable alias", Tools: []tool.Tool{scriptTool{name: "工具"}}},
		{Revision: "duplicate", Tools: []tool.Tool{executable}, DeferredTools: []tool.Tool{executable}},
		{Revision: "panic", Tools: []tool.Tool{capabilityPanicTool{mode: "direct"}}},
		{Revision: "panic", Tools: []tool.Tool{capabilityPanicTool{mode: "concurrent"}}},
	} {
		if set, err := NewToolSet(config); !errors.Is(err, ErrInvalidToolSet) || set.Configured() {
			t.Fatalf("invalid tool declaration admitted: %+v, %v", config, err)
		}
	}
	if (ToolSet{}).Configured() {
		t.Fatal("zero tool set reports executable authority")
	}
}

func TestDelegateConstructionAndOutputAdmissionUseTheBoundContract(t *testing.T) {
	compiler := conformancetest.Compiler(t)
	delegate := compiledDelegate(t, compiler, "worker", func(_ context.Context, value delegateValue) (delegateValue, error) { return value, nil })
	for _, config := range []DelegateConfig{
		{}, {Name: "worker", Description: "Missing child."},
		{Name: "Invalid Name", Description: "Invalid model name.", Child: delegate.child},
		{Name: "worker", Child: delegate.child},
	} {
		if value, err := NewDelegate(config); !errors.Is(err, ErrInvalidDelegate) || value.Valid() {
			t.Fatal("invalid delegate admitted", value, err)
		}
	}
	if err := (Delegate{}).validateInput(mustValue(run.EncodePayload(delegateValue{}))); !errors.Is(err, ErrInvalidDelegate) {
		t.Fatal(err)
	}
	for _, output := range []run.Payload{{}, mustValue(run.EncodePayload("wrong shape"))} {
		if err := delegate.validateOutput(output); err == nil {
			t.Fatal("delegate output contract was bypassed")
		}
	}
	if _, err := delegate.admitResultOutput(tool.NewTextOutput("untyped")); err == nil {
		t.Fatal("unstructured delegate output admitted")
	}
	if _, err := delegate.prepareInput(chat.ToolCall{ID: "bad", Name: "worker", Arguments: `{`}); err == nil {
		t.Fatal("malformed delegate arguments admitted")
	}
}

func TestMalformedDispatcherProtocolsFailAtTheCommittedBoundary(t *testing.T) {
	definition := mustValue(newDefinition(Config{Name: "test.rejection", Description: "Reject malformed dispatch requests."}))
	dispatcher := mustValue(newDispatcher(definition, Config{Model: CallModel(chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		t.Error("malformed request invoked a model")
		return nil, errors.New("must not run")
	}))}))
	for _, payload := range []string{`{}`, `{"model_call":{}}`, `{"tool_call":{"invocation":{"call":{"id":"call","name":"absent","arguments":"{}"}}}}`, `{"prepare_context":{"model_call_sequence":1,"request":{"messages":[{"role":"user","parts":[{"kind":"text","text":"question"}]}]}}}`} {
		effect := mustValue(run.NewDispatcherEffect([]byte(payload)))
		conformancetest.CheckDispatcherRejection(t, dispatcher, effect)
	}
	for _, payload := range []string{`{}`, `{"model_call":{}}`} {
		conformancetest.CheckDispatcherRejection(t, rejectingToolDispatcher{dispatcher: newToolDispatcher()}, mustValue(run.NewDispatcherEffect([]byte(payload))))
	}
}

type rejectingToolDispatcher struct{ dispatcher *toolDispatcher }

func (r rejectingToolDispatcher) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicyNever}
}

func (r rejectingToolDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, emit run.DeltaEmitter) (run.Settlement, error) {
	return r.dispatcher.Dispatch(ctx, request, emit)
}
