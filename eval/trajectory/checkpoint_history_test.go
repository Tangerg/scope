package trajectory_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/trajectory"
)

func TestCheckpointRecoveryCutCannotBecomeEvaluationEvidence(t *testing.T) {
	store := run.NewMemoryStore()
	runtime, err := run.New(t.Context(), run.Config{Store: store, Build: testBuild, CheckpointInterval: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := runtime.Close(context.WithoutCancel(t.Context())); closeErr != nil {
			t.Error(closeErr)
		}
	})
	model := &fixtureInteractionClient{usage: &chat.Usage{InputTokens: 3, OutputTokens: 2}}
	toolSet, err := interaction.NewToolSet(interaction.ToolSetConfig{Revision: "checkpoint", Tools: []tool.Tool{fixtureWeatherTool{}}})
	if err != nil {
		t.Fatal(err)
	}
	program, err := interaction.Compile(runtime.Compiler(), interaction.Config{
		Name: "test.checkpoint", Description: "Record a model-tool-model interaction.",
		Revision: "checkpoint", Model: interaction.CallModel(model), Tools: toolSet, MaxModelCalls: run.NewQuota(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := runtime.Start(t.Context(), program, run.StartRequest[interaction.Input]{
		ID:    run.StartID{Scope: "trajectory", Key: "checkpoint"},
		Input: interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("weather"))}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := handle.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	full := captureInteraction(t, handle)
	if !full.HistoryComplete() || full.RootUsage() != result.Usage() {
		t.Fatalf("captured root accounting differs from the committed result: %+v, %+v", full.RootUsage(), result.Usage())
	}
	usage, err := full.TreeUsage()
	if err != nil || usage != result.Usage() {
		t.Fatalf("captured tree accounting differs from its only process: %+v, %v", usage, err)
	}
	tokens, err := full.TotalTokens()
	if err != nil || tokens != 10 || len(full.ModelCalls()) != 2 || len(full.ToolCalls()) != 1 {
		t.Fatalf("full history lost calls or tokens: models=%d tools=%d tokens=%d error=%v", len(full.ModelCalls()), len(full.ToolCalls()), tokens, err)
	}
	cut, found, err := store.Load(t.Context(), handle.ID())
	if err != nil || !found || cut.Complete() {
		t.Fatalf("load did not produce a recovery cut: found=%v complete=%v error=%v", found, cut.Complete(), err)
	}
	if validateErr := cut.Validate(); validateErr != nil {
		t.Fatalf("valid recovery cut rejected by run: %v", validateErr)
	}
	config := trajectory.Config{Log: cut, Coverage: &trajectory.Coverage{}}
	if _, admissionErr := trajectory.New(config); !errors.Is(admissionErr, trajectory.ErrInvalidTrajectory) || !errors.Is(admissionErr, trajectory.ErrIncompleteRecording) {
		t.Fatalf("recovery cut admitted as a trajectory: %v", admissionErr)
	}
	encoded, err := jsonv2.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	unchanged := full
	if decodeErr := jsonv2.Unmarshal(encoded, &unchanged); !errors.Is(decodeErr, trajectory.ErrIncompleteRecording) || unchanged.RootUsage() != full.RootUsage() {
		t.Fatalf("recovery cut replaced valid trajectory evidence: %v", decodeErr)
	}
	head, err := cut.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if _, captureErr := trajectory.Capture(t.Context(), capturedSource{head: head, entries: cut.Entries}, &trajectory.Coverage{}); !errors.Is(captureErr, trajectory.ErrIncompleteRecording) {
		t.Fatalf("capture accepted a source missing its history prefix: %v", captureErr)
	}
	expected := trajectory.Expectation{
		Status: run.StatusCompleted,
		Tools:  &trajectory.ToolSequence{Calls: []trajectory.ToolExpectation{}},
		Limits: trajectory.Limits{CommittedSteps: new(uint64(0)), TotalTokens: new(int64(0))},
	}
	report, err := (trajectory.Evaluator{}).Evaluate(t.Context(), trajectory.Sample{Actual: full, Expected: expected})
	if err != nil || report.Verdict() != eval.VerdictFail {
		t.Fatalf("nonzero calls and usage passed a zero-work expectation: %v, %v", report.Verdict(), err)
	}
}
