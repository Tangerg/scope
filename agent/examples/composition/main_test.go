package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/run/runtest"
	"github.com/Tangerg/scope/core/chat"
)

func compositionValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func compositionTurn(step uint64, messages ...agentrun.Message) agentrun.Turn {
	return agentrun.Turn{Process: compositionValue(agentrun.ParseProcessID("composition-test")), Step: step, Messages: messages}
}

func TestRun(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	const want = "embedded: EMBEDDED\ncomposed: COMPOSITION | model: composition\n"
	if output.String() != want {
		t.Fatalf("output=%q, want %q", output.String(), want)
	}
}

func TestRunHonorsCancellationBeforeExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var output bytes.Buffer
	if err := run(ctx, &output); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal("canceled invocation published output")
	}
}

func TestUppercaseExecutionRejectsInvalidAdvancementAndRestoresExactly(t *testing.T) {
	program := compositionValue(newUppercaseProgram(conformancetest.Compiler(t)))
	input := compositionValue(agentrun.EncodePayload(textInput{Text: "once"}))
	execution := compositionValue(program.Definition().Start(input))
	before := compositionValue(execution.Snapshot())
	if _, err := execution.Step(t.Context(), compositionTurn(1, agentrun.Signal{})); !errors.Is(err, agentrun.ErrInvalidMessage) {
		t.Fatal(err)
	}
	if after := compositionValue(execution.Snapshot()); !after.Payload().Equal(before.Payload()) {
		t.Fatal("rejected message advanced state")
	}
	runtest.RunDefinitionConformance(t, runtest.DefinitionConformanceConfig{Definition: program.Definition(), Input: input, InitialTurn: compositionTurn(1)})
	transition := compositionValue(execution.Step(t.Context(), compositionTurn(1)))
	if transition.Kind() != agentrun.TransitionKindComplete {
		t.Fatal(transition.Kind())
	}
	completed := compositionValue(execution.Snapshot())
	if _, err := execution.Step(t.Context(), compositionTurn(2)); err == nil {
		t.Fatal("completed execution advanced")
	}
	if after := compositionValue(execution.Snapshot()); !after.Payload().Equal(completed.Payload()) {
		t.Fatal("duplicate completion changed state")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := program.Definition().Restore(ctx, before); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestDefinitionsRejectInvalidBindingsAndBoundaryValues(t *testing.T) {
	compiler := conformancetest.Compiler(t)
	model := compositionValue(newModelProgram(compiler))
	local := compositionValue(newUppercaseProgram(compiler))
	if _, err := newCompositionProgram(compiler, agentrun.Program[textInput, textOutput]{}, model); !errors.Is(err, agentrun.ErrInvalidProgram) {
		t.Fatal(err)
	}
	composed := compositionValue(newCompositionProgram(compiler, local, model))
	for _, row := range []struct {
		program agentrun.Program[agentrun.Payload, agentrun.Payload]
		input   agentrun.Payload
	}{
		{local.Erase(), compositionValue(agentrun.EncodePayload(textInput{Text: "x"}))},
		{composed.Erase(), compositionValue(agentrun.EncodePayload(compositionInput{Prompt: "x"}))},
	} {
		execution := compositionValue(row.program.Definition().Start(row.input))
		state := compositionValue(execution.Snapshot())
		extended := compositionValue(agentrun.ParsePayload(append(bytes.TrimSuffix(state.Payload().JSON(), []byte("}")), []byte(`,"unknown":true}`)...)))
		invalid := compositionValue(agentrun.NewExecutionState(state.Kind(), extended))
		if _, err := row.program.Definition().Restore(t.Context(), invalid); err == nil {
			t.Fatal("unknown state field admitted")
		}
	}
}

func TestCompositionRejectsUnaddressedInputAtAdmission(t *testing.T) {
	runtime := conformancetest.Runtime(t, agentrun.Config{})
	local := compositionValue(newUppercaseProgram(runtime.Compiler()))
	model := compositionValue(newModelProgram(runtime.Compiler()))
	program := compositionValue(newCompositionProgram(runtime.Compiler(), local, model))
	handle := compositionValue(runtime.Start(t.Context(), program, agentrun.StartRequest[compositionInput]{ID: agentrun.StartID{Scope: "test", Key: t.Name()}, Input: compositionInput{Prompt: "work"}}))
	signal := agentrun.Signal{ID: compositionValue(agentrun.ParseSignalID("unsolicited")), Payload: compositionValue(agentrun.EncodePayload("message"))}
	if _, err := handle.DeliverSignals(t.Context(), signal); err == nil {
		t.Fatal("workflow admitted undeclared external input")
	}
	output := compositionValue(handle.Await(t.Context()))
	if _, err := output.Output(); err != nil {
		t.Fatal(err)
	}
}

func TestCompositionRejectsMissingModelInputWithoutPublishingOutput(t *testing.T) {
	response, err := (compositionModel{}).Call(t.Context(), &chat.Request{})
	if response != nil || err == nil {
		t.Fatalf("response=%+v error=%v", response, err)
	}
}
