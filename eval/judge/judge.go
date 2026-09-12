// Package judge evaluates arbitrary subjects with a structured-output chat
// model. Subject validation and prompt construction remain owned by callers.
package judge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/eval"
)

const (
	metricJudgeConfigurationKey = "judge"
	reportSampleScoresKey       = "sample_scores"
)

const outputName = "eval_report"

type aggregation string

const aggregationMedian aggregation = "median"

// Prompt projects a domain subject into one valid judge message.
type Prompt[T any] func(T) (chat.Message, error)

// Config binds a portable metric and subject prompt to a structured-output
// model judge. Multiple samples use a deterministic median aggregation.
type Config[T any] struct {
	Model chat.Model
	// ModelID identifies the explicitly selected judge model and revision.
	ModelID string
	// RubricID versions the scoring rules implemented by Prompt. Callers must
	// change it when those rules change; a function cannot reveal its identity.
	RubricID string
	Metric   eval.Metric
	Prompt   Prompt[T]
	Options  chat.Options
	// Threshold is optional. Without one, evaluation produces a score without
	// inventing a pass/fail decision.
	Threshold *eval.Score
	Samples   int
}

type modelReport struct {
	Score    eval.Score `json:"score" jsonschema:"minimum=0,maximum=1"`
	Feedback string     `json:"feedback,omitzero"`
}

type metricConfiguration struct {
	ModelID     string       `json:"model_id"`
	RubricID    string       `json:"rubric_id"`
	Options     chat.Options `json:"options"`
	Aggregation aggregation  `json:"aggregation"`
	Samples     int          `json:"samples"`
	Threshold   *eval.Score  `json:"threshold,omitzero"`
}

// Evaluator asks a chat model for normalized scores without teaching the eval
// kernel any domain vocabulary.
type Evaluator[T any] struct {
	client    chatclient.Client
	format    chatclient.OutputFormat[modelReport]
	metric    eval.Metric
	prompt    Prompt[T]
	options   chat.Options
	threshold *eval.Score
	samples   int
}

// NewEvaluator freezes metric identity, options, threshold, and sampling policy.
func NewEvaluator[T any](config Config[T]) (*Evaluator[T], error) {
	if lo.IsNil(config.Model) {
		return nil, fmt.Errorf("%w: model is nil", eval.ErrInvalidEvaluatorConfig)
	}
	if err := config.Metric.Validate(); err != nil {
		return nil, fmt.Errorf("%w: metric: %w", eval.ErrInvalidEvaluatorConfig, err)
	}
	if config.Prompt == nil {
		return nil, fmt.Errorf("%w: prompt is nil", eval.ErrInvalidEvaluatorConfig)
	}
	if config.ModelID == "" || config.ModelID != strings.TrimSpace(config.ModelID) ||
		config.RubricID == "" || config.RubricID != strings.TrimSpace(config.RubricID) {
		return nil, fmt.Errorf("%w: model ID and rubric ID must be non-empty without surrounding whitespace", eval.ErrInvalidEvaluatorConfig)
	}
	if err := config.Options.Validate(); err != nil {
		return nil, fmt.Errorf("%w: options: %w", eval.ErrInvalidEvaluatorConfig, err)
	}
	var threshold *eval.Score
	if config.Threshold != nil {
		value := *config.Threshold
		if err := value.Validate(); err != nil {
			return nil, fmt.Errorf("%w: threshold: %w", eval.ErrInvalidEvaluatorConfig, err)
		}
		threshold = &value
	}
	if config.Samples < 0 {
		return nil, fmt.Errorf("%w: samples must not be negative", eval.ErrInvalidEvaluatorConfig)
	}
	samples := config.Samples
	if samples == 0 {
		samples = 1
	}
	metric, err := (metricConfiguration{
		ModelID: config.ModelID, RubricID: config.RubricID, Options: config.Options.Clone(),
		Aggregation: aggregationMedian, Samples: samples, Threshold: threshold,
	}).metric(config.Metric)
	if err != nil {
		return nil, fmt.Errorf("%w: metric configuration: %w", eval.ErrInvalidEvaluatorConfig, err)
	}
	client, err := chatclient.New(config.Model, chatclient.Config{})
	if err != nil {
		return nil, fmt.Errorf("%w: model: %w", eval.ErrInvalidEvaluatorConfig, err)
	}
	format, err := chatclient.JSONSchema[modelReport](chatclient.JSONSchemaConfig{Name: outputName})
	if err != nil {
		return nil, fmt.Errorf("%w: output format: %w", eval.ErrInvalidEvaluatorConfig, err)
	}
	return &Evaluator[T]{
		client: client, format: format, metric: metric, prompt: config.Prompt,
		options: config.Options.Clone(), threshold: threshold, samples: samples,
	}, nil
}

func (m metricConfiguration) metric(metric eval.Metric) (eval.Metric, error) {
	parameters := metric.Parameters()
	if err := parameters.Set(metricJudgeConfigurationKey, m); err != nil {
		return eval.Metric{}, err
	}
	return eval.NewMetric(eval.MetricConfig{
		Namespace:  metric.Namespace(),
		Name:       metric.Name(),
		Unit:       metric.Unit(),
		Direction:  metric.Direction(),
		Parameters: parameters,
	})
}

func (e *Evaluator[T]) Evaluate(ctx context.Context, subject T) (eval.Report, error) {
	if err := ctx.Err(); err != nil {
		return eval.Report{}, err
	}
	message, err := e.prompt(subject)
	if err != nil {
		return eval.Report{}, fmt.Errorf("eval/judge: build prompt: %w", err)
	}
	if err := message.Validate(); err != nil {
		return eval.Report{}, fmt.Errorf("eval/judge: prompt: %w", err)
	}

	outputs := make([]modelReport, e.samples)
	for index := range e.samples {
		output, callErr := e.client.Output(ctx, &chat.Request{
			Messages: []chat.Message{message.Clone()}, Options: e.options.Clone(),
		}, e.format)
		if callErr != nil {
			if errors.Is(callErr, chatclient.ErrInvalidOutput) {
				return eval.Report{}, fmt.Errorf("%w: model output: %w", eval.ErrInvalidReport, callErr)
			}
			return eval.Report{}, fmt.Errorf("eval/judge: sample %d: %w", index, callErr)
		}
		if err := output.Score.Validate(); err != nil {
			return eval.Report{}, fmt.Errorf("%w: sample %d score: %w", eval.ErrInvalidReport, index, err)
		}
		outputs[index] = output
	}
	return e.aggregate(outputs)
}

func (e *Evaluator[T]) aggregate(outputs []modelReport) (eval.Report, error) {
	slices.SortFunc(outputs, func(a, b modelReport) int {
		if a.Score < b.Score {
			return -1
		}
		if a.Score > b.Score {
			return 1
		}
		return 0
	})
	middle := len(outputs) / 2
	score := outputs[middle].Score
	feedback := strings.TrimSpace(outputs[middle].Feedback)
	if len(outputs)%2 == 0 {
		score = (outputs[middle-1].Score + outputs[middle].Score) / 2
		feedback = strings.TrimSpace(strings.Join([]string{outputs[middle-1].Feedback, outputs[middle].Feedback}, "\n\n"))
	}
	reportMetadata := metadata.Map{}
	if len(outputs) > 1 {
		scores := make([]eval.Score, len(outputs))
		for index := range outputs {
			scores[index] = outputs[index].Score
		}
		if err := reportMetadata.Set(reportSampleScoresKey, scores); err != nil {
			return eval.Report{}, fmt.Errorf("eval/judge: sample metadata: %w", err)
		}
	}
	verdict := eval.VerdictUnspecified
	if e.threshold != nil {
		decided, err := score.Verdict(*e.threshold)
		if err != nil {
			return eval.Report{}, fmt.Errorf("eval/judge: verdict: %w", err)
		}
		verdict = decided
	}
	report := eval.Report{
		Metric: e.metric, Verdict: verdict, Score: &score,
		Feedback: feedback, Metadata: reportMetadata,
	}
	if err := report.Validate(); err != nil {
		return eval.Report{}, fmt.Errorf("eval/judge: report: %w", err)
	}
	return report, nil
}

var _ eval.Evaluator[struct{}] = (*Evaluator[struct{}])(nil)
