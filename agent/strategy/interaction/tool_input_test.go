package interaction_test

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

// A Tool child reuses one logical input WaitKey for every pause. The second
// wait can only open because the first wait's answer was consumed and closed,
// so this exercises the removal of per-pause wait identity.
func TestToolInputResumesSequentiallyReusingOneWait(t *testing.T) {
	executable, err := newContinuingTool(tool.FuncConfig{
		Name: "ask_twice", Description: "Ask for input twice before completing.",
	}, func(ctx context.Context, callInput tool.Invocation) (tool.Result, error) {
		continuation, resumed := callInput.Continuation()
		if !resumed {
			return waitingInput(
				json.RawMessage(`{"question":"first?"}`), json.RawMessage(`{"type":"string","minLength":1}`), json.RawMessage(`{"stage":1}`))
		}
		var state struct {
			Stage int `json:"stage"`
		}
		if err := jsonv2.Unmarshal(continuation.State(), &state); err != nil {
			return tool.Result{}, err
		}
		if state.Stage == 1 {
			return waitingInput(
				json.RawMessage(`{"question":"second?"}`), json.RawMessage(`{"type":"string","minLength":1}`), json.RawMessage(`{"stage":2}`))
		}
		return tool.Result{Output: chat.NewTextToolOutput("done")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		if request.Messages[len(request.Messages)-1].Role == chat.RoleTool {
			return textResponse("done"), nil
		}
		return toolCallResponse(chat.ToolCall{ID: "ask", Name: "ask_twice", Arguments: `{}`}), nil
	})
	deployment := newDeployment(t, model, []tool.Tool{executable}, 2)
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	root, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "greet"))
	if err != nil {
		t.Fatal(err)
	}
	var answered []agent.WaitID
	for stage, answer := range []string{"first", "second"} {
		pending := nextToolInputWait(t, ctx, engine, root, answered)
		answered = append(answered, pending.WaitID())
		id, idErr := agent.ParseSignalID(fmt.Sprintf("signal:answer-%d", stage))
		if idErr != nil {
			t.Fatal(idErr)
		}
		signal, signalErr := pending.ResponseSignal(id, json.RawMessage(`"`+answer+`"`))
		if signalErr != nil {
			t.Fatal(signalErr)
		}
		child := pendingToolProcess(t, engine, pending)
		if accepted, deliveryErr := child.DeliverSignals(ctx, signal); deliveryErr != nil || !accepted {
			t.Fatalf("stage %d answer accepted=%t error=%v", stage, accepted, deliveryErr)
		}
	}
	result, err := root.Await(ctx)
	if err != nil || result.Termination().Status() != agent.StatusCompleted {
		t.Fatalf("result status=%s error=%v", result.Termination().Status(), err)
	}
	if len(answered) != 2 || answered[0] == answered[1] || !answered[0].Valid() || !answered[1].Valid() {
		t.Fatalf("sequential wait identities = %v", answered)
	}
}

// nextToolInputWait returns the pending Tool input once it advances past every
// WaitID already answered, so a not-yet-consumed prior wait is never re-answered.
func nextToolInputWait(t *testing.T, ctx context.Context, engine *agent.Engine, root *agent.Process, answered []agent.WaitID) interaction.PendingToolInput {
	t.Helper()
	for ctx.Err() == nil {
		_, pending := captureToolInput(t, engine, root)
		if !slicesContainsWaitID(answered, pending.WaitID()) {
			return pending
		}
		runtime.Gosched()
	}
	t.Fatal(ctx.Err())
	return interaction.PendingToolInput{}
}

func slicesContainsWaitID(ids []agent.WaitID, id agent.WaitID) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

func TestInputRequestRejectsInvalidAndOversizedJSON(t *testing.T) {
	for name, value := range map[string]string{
		"duplicate member": `{"answer":1,"answer":2}`,
		"trailing value":   `1 2`,
		"invalid unicode":  `"\ud800"`,
		"normalized size":  `"` + strings.Repeat("<", (1<<20)/6+1) + `"`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newInputRequest(json.RawMessage(value), json.RawMessage(`true`), json.RawMessage(`null`))
			if !errors.Is(err, tool.ErrInvalidInputRequest) {
				t.Fatalf("error = %v, want tool.ErrInvalidInputRequest", err)
			}
		})
	}
}

func TestPendingToolInputsTracksPausedWaitUntilAnswered(t *testing.T) {
	waiting := newInputRequestTool()
	waiting.Release()
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		if request.Messages[len(request.Messages)-1].Role == chat.RoleTool {
			return textResponse("done"), nil
		}
		return toolCallResponse(chat.ToolCall{ID: "ask", Name: "ask_name", Arguments: `{}`}), nil
	})
	deployment := newDeployment(t, model, []tool.Tool{waiting}, 2)
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	root, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "greet"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer stop()
		if killErr := root.Kill(cleanup, "test complete"); killErr != nil && !errors.Is(killErr, agent.ErrProcessFinished) {
			t.Error(killErr)
		}
		if joinErr := root.Join(cleanup); joinErr != nil {
			t.Error(joinErr)
		}
	})
	_, pending := captureToolInput(t, engine, root)
	child := pendingToolProcess(t, engine, pending)
	if pauseErr := child.Pause(ctx, "hold input continuation"); pauseErr != nil {
		t.Fatal(pauseErr)
	}
	waitForStatus(t, engine, child, agent.StatusPaused)
	tree, err := engine.CaptureTree(ctx, root.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	tree, err = agent.ParseTreeSnapshot(tree.JSON())
	if err != nil {
		t.Fatal(err)
	}
	inputs, err := interaction.PendingToolInputs(tree)
	if err != nil || len(inputs) != 1 {
		t.Fatalf("paused Tool inputs=%v error=%v", inputs, err)
	}
	if inputs[0].ProcessID() != child.Relation().ProcessID() || inputs[0].WaitID() != pending.WaitID() ||
		string(inputs[0].Prompt()) != string(pending.Prompt()) || string(inputs[0].ResponseSchema()) != string(pending.ResponseSchema()) {
		t.Fatal("pause changed the pending Tool input")
	}
	id, err := agent.ParseSignalID("signal:paused-tool-answer")
	if err != nil {
		t.Fatal(err)
	}
	answer, err := inputs[0].ResponseSignal(id, json.RawMessage(`"Ada"`))
	if err != nil {
		t.Fatal(err)
	}
	if accepted, deliveryErr := child.DeliverSignals(ctx, answer); deliveryErr != nil || !accepted {
		t.Fatalf("paused Tool answer=%t error=%v", accepted, deliveryErr)
	}
	tree, err = engine.CaptureTree(ctx, root.Relation().ProcessID())
	if err != nil {
		t.Fatal(err)
	}
	inputs, err = interaction.PendingToolInputs(tree)
	if err != nil || len(inputs) != 0 {
		t.Fatalf("answered Tool inputs=%v error=%v", inputs, err)
	}
	if inspectProcessSnapshot(t, engine, child).Status() != agent.StatusPaused || waiting.continuationCalls.Load() != 0 {
		t.Fatal("answer released the Tool pause")
	}
	if resumeErr := child.Resume(ctx); resumeErr != nil {
		t.Fatal(resumeErr)
	}
	result, err := root.Await(ctx)
	if err != nil || result.Termination().Status() != agent.StatusCompleted || waiting.initialCalls.Load() != 1 || waiting.continuationCalls.Load() != 1 {
		t.Fatalf("result=%s error=%v Tool calls=%d/%d", result.Termination().Status(), err, waiting.initialCalls.Load(), waiting.continuationCalls.Load())
	}
}

func TestToolInputResponsePreservesNumbersAcrossRestore(t *testing.T) {
	for _, sample := range []struct {
		name     string
		facade   bool
		response string
	}{
		{name: "primitive", response: "9007199254740993"},
		{name: "facade", facade: true, response: "9007199254740993"},
		{name: "invalid primitive response", response: "9007199254740992"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			testToolInputRestore(t, sample.facade, `{"const":9007199254740993}`, "9007199254740993", "9007199254740992", sample.response)
		})
	}
}

func TestToolInputResponsePreservesSchemaResourcesAcrossRestore(t *testing.T) {
	type answerValue struct {
		Text string `json:"text"`
	}
	type answer struct {
		Nested answerValue `json:"nested"`
	}
	derived, err := jsonschema.For[answer]()
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		name, schema, value, invalid string
	}{
		{name: "derived definitions", schema: string(derived.JSON()), value: `{"nested":{"text":"valid"}}`, invalid: `{"nested":{"text":1}}`},
		{name: "recursive definition", schema: `{"$defs":{"node":{"type":"object","properties":{"value":{"type":"integer"},"next":{"anyOf":[{"type":"null"},{"$ref":"#/$defs/node"}]}},"required":["value","next"],"additionalProperties":false}},"$ref":"#/$defs/node"}`, value: `{"value":9007199254740993,"next":{"value":1,"next":null}}`, invalid: `{"value":"bad","next":null}`},
		{name: "resource anchor", schema: `{"$id":"urn:answer:root","$defs":{"value":{"$anchor":"answer","const":"valid"}},"$ref":"#answer"}`, value: `"valid"`, invalid: `"invalid"`},
		{name: "input_response business member", schema: `{"type":"object","properties":{"input_response":{"const":"valid"}},"required":["input_response"],"additionalProperties":false}`, value: `{"input_response":"valid"}`, invalid: `{"input_response":"invalid"}`},
	} {
		t.Run(sample.name, func(t *testing.T) {
			for _, facade := range []bool{false, true} {
				t.Run(fmt.Sprintf("facade=%t", facade), func(t *testing.T) {
					testToolInputRestore(t, facade, sample.schema, sample.value, sample.invalid, sample.value)
				})
			}
			t.Run("reject then accept", func(t *testing.T) {
				testToolInputRestore(t, false, sample.schema, sample.value, sample.invalid, sample.invalid)
			})
		})
	}
}

func testToolInputRestore(t *testing.T, facade bool, schema, value, invalid, response string) {
	t.Helper()
	compiled, err := jsonschema.Parse([]byte(schema))
	if err != nil {
		t.Fatal(err)
	}
	schema = string(compiled.JSON())
	normalize := func(raw string) string {
		payload, parseErr := agent.ParsePayload([]byte(raw))
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		return string(payload.JSON())
	}
	value, response = normalize(value), normalize(response)
	resumed := make(chan tool.Continuation, 1)
	executable, err := newContinuingTool(tool.FuncConfig{
		Name: "confirm_number", Description: "Confirm an exact numeric value.",
	}, func(ctx context.Context, callInput tool.Invocation) (tool.Result, error) {
		continuation, ok := callInput.Continuation()
		if !ok {
			return waitingInput(json.RawMessage(value), json.RawMessage(schema), json.RawMessage(value))
		}
		resumed <- continuation
		return tool.Result{Output: chat.NewTextToolOutput("confirmed")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	model := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		if request.Messages[len(request.Messages)-1].Role == chat.RoleTool {
			return textResponse("confirmed"), nil
		}
		return toolCallResponse(chat.ToolCall{ID: "confirm", Name: "confirm_number", Arguments: `{}`}), nil
	})
	deployment := newDeployment(t, model, []tool.Tool{executable}, 2)
	store := agent.NewMemoryTreeCommitter()
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	process, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "confirm"))
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if process != nil {
			if _, awaitErr := process.Await(cleanup); awaitErr != nil {
				t.Error(awaitErr)
			}
		}
		if closeErr := engine.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	tree, pending := captureToolInput(t, engine, process)
	tree, err = agent.ParseTreeSnapshot(tree.JSON())
	if err != nil {
		t.Fatal(err)
	}
	oldEngine, oldProcess := engine, process
	engine, err = agent.NewEngine(agent.EngineConfig{TreeCommitter: store})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { retireTestWriter(t, oldEngine, oldProcess) })
	process, err = engine.RestoreTree(ctx, deployment.Deployment, tree)
	if err != nil {
		t.Fatal(err)
	}
	toolProcess := pendingToolProcess(t, engine, pending)
	if string(pending.Prompt()) != value || string(pending.ResponseSchema()) != schema {
		t.Fatalf("restored prompt=%s schema=%s", pending.Prompt(), pending.ResponseSchema())
	}
	id, err := agent.ParseSignalID("signal:numeric-answer")
	if err != nil {
		t.Fatal(err)
	}
	if _, responseErr := pending.ResponseSignal(id, json.RawMessage(invalid)); !errors.Is(responseErr, tool.ErrInvalidInputRequest) {
		t.Fatalf("adjacent integer validation error=%v", responseErr)
	}
	var signal agent.SignalRequest
	if facade {
		signal, err = pending.ResponseSignal(id, json.RawMessage(response))
	} else {
		signal, err = interaction.NewToolInputResponseSignal(id, pending.WaitID(), json.RawMessage(response))
	}
	if err != nil {
		t.Fatal(err)
	}
	accepted, deliveryErr := toolProcess.DeliverSignals(ctx, signal)
	if response != value {
		// The wait owns the response schema, so a schema-invalid answer is rejected
		// at admission and keeps the wait open; a valid answer then still completes.
		if accepted || !errors.Is(deliveryErr, agent.ErrSignalRejected) {
			t.Fatalf("invalid answer accepted=%t error=%v", accepted, deliveryErr)
		}
		validID, parseErr := agent.ParseSignalID("signal:valid-answer")
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		valid, validErr := interaction.NewToolInputResponseSignal(validID, pending.WaitID(), json.RawMessage(value))
		if validErr != nil {
			t.Fatal(validErr)
		}
		if a, e := toolProcess.DeliverSignals(ctx, valid); e != nil || !a {
			t.Fatalf("valid answer after rejection accepted=%t error=%v", a, e)
		}
	} else if deliveryErr != nil || !accepted {
		t.Fatalf("response accepted=%t error=%v", accepted, deliveryErr)
	}
	result, err := process.Await(ctx)
	if err != nil || result.Termination().Status() != agent.StatusCompleted {
		t.Fatalf("result status=%s error=%v", result.Termination().Status(), err)
	}
	continuation := <-resumed
	if string(continuation.State()) != value || string(continuation.Response()) != value {
		t.Fatalf("resumed state=%s response=%s", continuation.State(), continuation.Response())
	}
}
