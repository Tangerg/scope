package eval

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/metadata"
)

// PassPolicy controls categorical aggregation independently from score weights.
type PassPolicy string

// Composite pass policies remain explicit so required components and minimum
// counts cannot be encoded in magic thresholds.
const (
	PassNone    PassPolicy = ""
	PassAll     PassPolicy = "all"
	PassAny     PassPolicy = "any"
	PassAtLeast PassPolicy = "at_least"
)

const compositePolicy = "composite"

func (p PassPolicy) normalize() (PassPolicy, error) {
	switch p {
	case PassNone, PassAll, PassAny, PassAtLeast:
		return p, nil
	default:
		return "", fmt.Errorf("%w: unsupported pass policy %q", ErrInvalidEvaluatorConfig, p)
	}
}

func (p PassPolicy) minimum(componentCount, configured int) (int, error) {
	switch p {
	case PassNone:
		if configured != 0 {
			return 0, fmt.Errorf("%w: minimum passed requires the at_least policy", ErrInvalidEvaluatorConfig)
		}
		return 0, nil
	case PassAll:
		if configured != 0 {
			return 0, fmt.Errorf("%w: minimum passed is only valid with the at_least policy", ErrInvalidEvaluatorConfig)
		}
		return componentCount, nil
	case PassAny:
		if configured != 0 {
			return 0, fmt.Errorf("%w: minimum passed is only valid with the at_least policy", ErrInvalidEvaluatorConfig)
		}
		return 1, nil
	case PassAtLeast:
		if configured <= 0 || configured > componentCount {
			return 0, fmt.Errorf("%w: minimum passed must be between 1 and %d", ErrInvalidEvaluatorConfig, componentCount)
		}
		return configured, nil
	default:
		return 0, fmt.Errorf("%w: unsupported pass policy %q", ErrInvalidEvaluatorConfig, p)
	}
}

// Component assigns score weight and pass criticality to one evaluator.
// A zero Weight selects 1. Required components must pass independently of the
// aggregate pass policy and still contribute to the score. To keep a gate out
// of the quality score, declare it as another assessment in a [Suite]. Required
// is only valid when CompositeConfig explicitly selects a pass policy.
type Component[T any] struct {
	Evaluator Evaluator[T]
	Weight    float64
	Required  bool
}

// CompositeConfig defines score aggregation and an optional categorical policy.
// A zero PassPolicy produces only a score and accepts score-only components.
// A zero MaxConcurrency selects DefaultMaxConcurrency.
type CompositeConfig[T any] struct {
	Components     []Component[T]
	PassPolicy     PassPolicy
	MinimumPassed  int
	MaxConcurrency int
}

// CompositeEvaluator combines scored child reports. A configured pass policy
// additionally requires decided children; it does not change score identity.
type CompositeEvaluator[T any] struct {
	components     []Component[T]
	passPolicy     PassPolicy
	minimumPassed  int
	maxConcurrency int
}

// NewCompositeEvaluator snapshots components and resolves all zero-value
// defaults before evaluation begins.
func NewCompositeEvaluator[T any](config CompositeConfig[T]) (*CompositeEvaluator[T], error) {
	if len(config.Components) == 0 {
		return nil, fmt.Errorf("%w: at least one component is required", ErrInvalidEvaluatorConfig)
	}
	if config.MaxConcurrency < 0 {
		return nil, fmt.Errorf("%w: maximum concurrency must not be negative", ErrInvalidEvaluatorConfig)
	}

	components := make([]Component[T], len(config.Components))
	for index, component := range config.Components {
		if lo.IsNil(component.Evaluator) {
			return nil, fmt.Errorf("%w: components[%d] evaluator is nil", ErrInvalidEvaluatorConfig, index)
		}
		if math.IsNaN(component.Weight) || math.IsInf(component.Weight, 0) || component.Weight < 0 {
			return nil, fmt.Errorf("%w: components[%d] weight must be finite and non-negative", ErrInvalidEvaluatorConfig, index)
		}
		if component.Weight == 0 {
			component.Weight = 1
		}
		if component.Required && config.PassPolicy == PassNone {
			return nil, fmt.Errorf("%w: required components need an explicit pass policy", ErrInvalidEvaluatorConfig)
		}
		components[index] = component
	}

	policy, err := config.PassPolicy.normalize()
	if err != nil {
		return nil, err
	}
	minimumPassed, err := policy.minimum(len(components), config.MinimumPassed)
	if err != nil {
		return nil, err
	}

	maxConcurrency := config.MaxConcurrency
	if maxConcurrency == 0 {
		maxConcurrency = DefaultMaxConcurrency
	}
	if maxConcurrency > len(components) {
		maxConcurrency = len(components)
	}
	return &CompositeEvaluator[T]{
		components: components, passPolicy: policy, minimumPassed: minimumPassed,
		maxConcurrency: maxConcurrency,
	}, nil
}

func (c *CompositeEvaluator[T]) Evaluate(ctx context.Context, subject T) (Report, error) {
	evaluators := make([]Evaluator[T], len(c.components))
	for index, component := range c.components {
		evaluators[index] = component.Evaluator
	}
	reports, err := evaluateAll(ctx, evaluators, c.maxConcurrency, subject)
	if err != nil {
		return Report{}, err
	}
	for index, report := range reports {
		if report.Score == nil {
			return Report{}, fmt.Errorf("eval: component %d: %w: composite components require a score", index, ErrInvalidReport)
		}
		if c.passPolicy != PassNone && report.Decision == nil {
			return Report{}, fmt.Errorf("eval: component %d: %w: pass policy requires a decision", index, ErrInvalidReport)
		}
	}
	return c.combine(reports)
}

func (c *CompositeEvaluator[T]) combine(reports []Report) (Report, error) {
	metric, err := c.metricFor(reports)
	if err != nil {
		return Report{}, err
	}
	combined := Report{Metric: metric, Details: reports}
	verdict := VerdictPass
	feedback := make([]string, 0, len(reports))
	weightScale := 0.0
	for _, component := range c.components {
		weightScale = max(weightScale, component.Weight)
	}
	_, weightExponent := math.Frexp(weightScale)
	passed := 0
	totalWeight := 0.0
	weightedScore := 0.0
	for index, report := range reports {
		component := c.components[index]
		if report.Verdict() == VerdictPass {
			passed++
		} else if component.Required {
			verdict = VerdictFail
		}
		// Binary scaling bounds the sum without rounding ordinary weights.
		weight := math.Ldexp(component.Weight, -weightExponent)
		weightedScore += report.Score.Float64() * weight
		totalWeight += weight
		if report.Feedback != "" {
			feedback = append(feedback, report.Feedback)
		}
	}
	if passed < c.minimumPassed {
		verdict = VerdictFail
	}
	if c.passPolicy != PassNone {
		decision, err := c.decisionFor(reports, verdict)
		if err != nil {
			return Report{}, err
		}
		combined.Decision = &decision
	}
	score := Score(weightedScore / totalWeight)
	combined.Score = &score
	combined.Feedback = strings.Join(feedback, "\n\n")
	if err := combined.Validate(); err != nil {
		return Report{}, err
	}
	return combined, nil
}

type componentIdentity struct {
	Metric Metric  `json:"metric"`
	Weight float64 `json:"weight"`
}

type compositeMetricIdentity struct {
	Components []componentIdentity `json:"components"`
}

func (c *CompositeEvaluator[T]) metricFor(reports []Report) (Metric, error) {
	components := make([]componentIdentity, len(reports))
	for index, report := range reports {
		components[index] = componentIdentity{
			Metric: report.Metric, Weight: c.components[index].Weight,
		}
	}
	parameters := metadata.Map{}
	identity := compositeMetricIdentity{
		Components: components,
	}
	if err := parameters.Set(metricConfigurationKey, identity); err != nil {
		return Metric{}, fmt.Errorf("eval: composite metric identity: %w", err)
	}
	metric, err := NewMetric(MetricConfig{Name: MetricNameComposite, Parameters: parameters})
	if err != nil {
		return Metric{}, fmt.Errorf("eval: composite metric identity: %w", err)
	}
	return metric, nil
}

func (c *CompositeEvaluator[T]) decisionFor(reports []Report, verdict Verdict) (Decision, error) {
	type componentRule struct {
		Policy     string       `json:"policy"`
		Parameters metadata.Map `json:"parameters,omitzero"`
		Required   bool         `json:"required,omitzero"`
	}
	rules := make([]componentRule, len(reports))
	for index, report := range reports {
		rules[index] = componentRule{
			Policy: report.Decision.Policy, Parameters: report.Decision.Parameters,
			Required: c.components[index].Required,
		}
	}
	parameters := metadata.Map{}
	if err := parameters.Set(metricConfigurationKey, struct {
		Components    []componentRule `json:"components"`
		PassPolicy    PassPolicy      `json:"pass_policy"`
		MinimumPassed int             `json:"minimum_passed"`
	}{Components: rules, PassPolicy: c.passPolicy, MinimumPassed: c.minimumPassed}); err != nil {
		return Decision{}, fmt.Errorf("eval: composite decision identity: %w", err)
	}
	return Decision{Policy: compositePolicy, Parameters: parameters, Verdict: verdict}, nil
}
