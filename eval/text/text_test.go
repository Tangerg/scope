package text_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/eval"
	texteval "github.com/Tangerg/scope/eval/text"
)

func TestModelEvaluatorConstructionValidatesConfiguration(t *testing.T) {
	if _, err := texteval.NewGroundednessEvaluator(texteval.ModelEvaluatorConfig{}); !errors.Is(err, eval.ErrInvalidEvaluatorConfig) {
		t.Fatalf("nil model error = %v", err)
	}
	model := &fakeModel{reply: `{"score":0.5}`}
	negative := eval.Score(-0.1)
	if _, err := texteval.NewGroundednessEvaluator(texteval.ModelEvaluatorConfig{
		ModelID: "test-model-v1", Model: model, Threshold: &negative,
	}); !errors.Is(err, eval.ErrInvalidEvaluatorConfig) {
		t.Fatalf("negative threshold error = %v", err)
	}
	large := eval.Score(1.1)
	if _, err := texteval.NewGroundednessEvaluator(texteval.ModelEvaluatorConfig{
		ModelID: "test-model-v1", Model: model, Threshold: &large,
	}); !errors.Is(err, eval.ErrInvalidEvaluatorConfig) {
		t.Fatalf("large threshold error = %v", err)
	}
	missing, err := chatclient.ParseTemplate("{{.Missing}}")
	if err != nil {
		t.Fatal(err)
	}
	if _, constructErr := texteval.NewGroundednessEvaluator(texteval.ModelEvaluatorConfig{
		ModelID: "test-model-v1", Model: model, PromptTemplate: missing,
	}); !errors.Is(constructErr, eval.ErrInvalidEvaluatorConfig) {
		t.Fatalf("unknown field error = %v", constructErr)
	}
	unsupportedContext, err := chatclient.ParseTemplate("{{.Input}} {{.Output}} {{.Context}}")
	if err != nil {
		t.Fatal(err)
	}
	if _, constructErr := texteval.NewAnswerRelevanceEvaluator(texteval.ModelEvaluatorConfig{
		ModelID: "test-model-v1", Model: model, PromptTemplate: unsupportedContext,
	}); !errors.Is(constructErr, eval.ErrInvalidEvaluatorConfig) {
		t.Fatalf("unsupported relevance prompt field error = %v", constructErr)
	}
	var typedNilModel *fakeModel
	if _, err := texteval.NewGroundednessEvaluator(texteval.ModelEvaluatorConfig{
		Model: typedNilModel,
	}); !errors.Is(err, eval.ErrInvalidEvaluatorConfig) {
		t.Fatalf("typed nil model error = %v", err)
	}
}

func TestGroundednessBuildsStructuredRequestAndDecodesResult(t *testing.T) {
	model := &fakeModel{reply: `{"score":0.95,"feedback":"Fully supported."}`}
	threshold := eval.Score(0.5)
	evaluator, err := texteval.NewGroundednessEvaluator(texteval.ModelEvaluatorConfig{
		ModelID: "test-model-v1", Model: model, Threshold: &threshold,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluator.Evaluate(t.Context(), texteval.GroundednessSample{
		Output: "the claim", Evidence: []string{"source one", "", "source two"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != eval.VerdictPass || result.Score == nil || *result.Score != 0.95 || result.Feedback != "Fully supported." {
		t.Fatalf("result = %#v", result)
	}
	request := model.lastRequest()
	if request == nil || len(request.Messages) != 1 || request.Messages[0].Role != chat.RoleUser {
		t.Fatalf("model request = %#v", request)
	}
	prompt := request.Messages[0].Text()
	for _, want := range []string{"source one\nsource two", "the claim"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt %q missing %q", prompt, want)
		}
	}
	if request.Options.OutputFormat == nil || request.Options.OutputFormat.Type != chat.OutputFormatJSONSchema {
		t.Fatalf("output format = %#v, want JSON Schema", request.Options.OutputFormat)
	}
}

func TestAnswerRelevanceSupportsCustomPromptAndThreshold(t *testing.T) {
	model := &fakeModel{reply: `{"score":0.6,"feedback":"Partly relevant."}`}
	prompt, err := chatclient.ParseTemplate("Q={{.Input}} A={{.Output}}")
	if err != nil {
		t.Fatal(err)
	}
	threshold := eval.Score(0.8)
	evaluator, err := texteval.NewAnswerRelevanceEvaluator(texteval.ModelEvaluatorConfig{
		ModelID: "test-model-v1", Model: model, Threshold: &threshold, PromptTemplate: prompt,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluator.Evaluate(t.Context(), texteval.AnswerRelevanceSample{
		Input: "question", Output: "answer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != eval.VerdictFail || result.Score == nil || *result.Score != 0.6 || result.Feedback != "Partly relevant." {
		t.Fatalf("result = %#v", result)
	}
	if got := model.lastRequest().Messages[0].Text(); got != "Q=question A=answer" {
		t.Fatalf("custom prompt = %q", got)
	}
}

func TestModelEvaluatorsRejectMissingSemanticInputs(t *testing.T) {
	model := &fakeModel{reply: `{"score":0.5}`}
	groundedness, err := texteval.NewGroundednessEvaluator(texteval.ModelEvaluatorConfig{ModelID: "test-model-v1", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []texteval.GroundednessSample{
		{Evidence: []string{"source"}},
		{Output: "answer"},
		{Output: "answer", Evidence: []string{"  "}},
	} {
		if _, evaluateErr := groundedness.Evaluate(t.Context(), sample); !errors.Is(evaluateErr, texteval.ErrInvalidSample) {
			t.Fatalf("Groundedness Evaluate(%#v) error = %v", sample, evaluateErr)
		}
	}
	relevance, err := texteval.NewAnswerRelevanceEvaluator(texteval.ModelEvaluatorConfig{ModelID: "test-model-v1", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := relevance.Evaluate(t.Context(), texteval.AnswerRelevanceSample{Output: "answer"}); !errors.Is(err, texteval.ErrInvalidSample) {
		t.Fatalf("missing input error = %v", err)
	}
	if calls := model.callCount(); calls != 0 {
		t.Fatalf("model calls after invalid inputs = %d", calls)
	}
}

func TestCorrectnessUsesReferenceAndSupportsSelfConsistency(t *testing.T) {
	model := &fakeModel{reply: "{\"score\":0.9,\"feedback\":\"Correct.\"}"}
	threshold := eval.Score(0.5)
	evaluator, err := texteval.NewCorrectnessEvaluator(texteval.ModelEvaluatorConfig{
		ModelID: "test-model-v1", Model: model, Threshold: &threshold, Samples: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := evaluator.Evaluate(t.Context(), texteval.CorrectnessSample{
		Input: "2 + 2", Output: "4", Reference: "4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verdict != eval.VerdictPass || report.Score == nil || *report.Score != 0.9 || model.callCount() != 3 {
		t.Fatalf("report = %#v, calls = %d", report, model.callCount())
	}
	prompt := model.lastRequest().Messages[0].Text()
	for _, want := range []string{"2 + 2", "Reference:", "4"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt %q missing %q", prompt, want)
		}
	}
	if _, err := evaluator.Evaluate(t.Context(), texteval.CorrectnessSample{
		Input: "2 + 2", Output: "4",
	}); !errors.Is(err, texteval.ErrInvalidSample) {
		t.Fatalf("missing reference error = %v", err)
	}
}

func TestModelEvaluatorPreservesCancellationAndModelErrors(t *testing.T) {
	modelErr := errors.New("model failed")
	model := &fakeModel{err: modelErr}
	evaluator, err := texteval.NewGroundednessEvaluator(texteval.ModelEvaluatorConfig{ModelID: "test-model-v1", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	sample := texteval.GroundednessSample{Output: "answer", Evidence: []string{"source"}}
	if _, err := evaluator.Evaluate(t.Context(), sample); !errors.Is(err, modelErr) {
		t.Fatalf("model error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := model.callCount()
	if _, err := evaluator.Evaluate(ctx, sample); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if calls := model.callCount(); calls != before {
		t.Fatalf("model called after cancellation: before %d after %d", before, calls)
	}
}

func TestModelEvaluatorRejectsInvalidStructuredResults(t *testing.T) {
	for _, reply := range []string{"YES", "5 out of 10", "", `{"score":"0.5"}`} {
		model := &fakeModel{reply: reply}
		evaluator, err := texteval.NewGroundednessEvaluator(texteval.ModelEvaluatorConfig{ModelID: "test-model-v1", Model: model})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := evaluator.Evaluate(t.Context(), texteval.GroundednessSample{
			Output: "answer", Evidence: []string{"source"},
		}); err == nil {
			t.Fatalf("reply %q was accepted", reply)
		}
	}
	model := &fakeModel{reply: `{"score":2}`}
	evaluator, err := texteval.NewGroundednessEvaluator(texteval.ModelEvaluatorConfig{ModelID: "test-model-v1", Model: model})
	if err != nil {
		t.Fatal(err)
	}
	_, err = evaluator.Evaluate(t.Context(), texteval.GroundednessSample{Output: "answer", Evidence: []string{"source"}})
	if !errors.Is(err, eval.ErrInvalidReport) {
		t.Fatalf("out-of-range score error = %v", err)
	}
}

func TestGroundednessSampleCloneOwnsEvidence(t *testing.T) {
	sample := texteval.GroundednessSample{Output: "output", Evidence: []string{"first"}}
	clone := sample.Clone()
	clone.Evidence[0] = "clone"
	if got := sample.EvidenceText(); got != "first" {
		t.Fatalf("EvidenceText = %q, want first", got)
	}
}

type fakeModel struct {
	mu      sync.Mutex
	reply   string
	err     error
	request *chat.Request
	calls   int
}

func (f *fakeModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.request = request
	if f.err != nil {
		return nil, f.err
	}
	if f.reply == "" {
		return &chat.Response{}, nil
	}
	message := chat.NewAssistantMessage(chat.NewTextPart(f.reply))
	return &chat.Response{Output: &chat.Output{
		Message: &message, FinishReason: chat.FinishReasonStop,
	}}, nil
}

func (f *fakeModel) lastRequest() *chat.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.request
}

func (f *fakeModel) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}
