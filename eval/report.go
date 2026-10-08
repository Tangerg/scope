package eval

import (
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Tangerg/scope/core/metadata"
)

// MaxReportDepth bounds recursive detail trees at every public trust boundary.
const MaxReportDepth = 64

// Report is one evaluation result. Decision, Score, and Measurement are
// independent and optional, so no evaluation has to invent a threshold or a
// score. Details are supporting evidence, never additional summary observations.
type Report struct {
	Metric      Metric       `json:"metric"`
	Decision    *Decision    `json:"decision,omitzero"`
	Score       *Score       `json:"score,omitzero"`
	Measurement *float64     `json:"measurement,omitzero"`
	Feedback    string       `json:"feedback,omitzero"`
	Metadata    metadata.Map `json:"metadata,omitzero"`
	Details     []Report     `json:"details,omitzero"`
}

func (r Report) Verdict() Verdict {
	if r.Decision == nil {
		return VerdictUnspecified
	}
	return r.Decision.Verdict
}

// Clone validates the complete detail tree before allocating its detached copy.
func (r Report) Clone() (Report, error) {
	if err := r.Validate(); err != nil {
		return Report{}, err
	}
	return r.cloneValid(), nil
}

func (r Report) cloneValid() Report {
	if r.Decision != nil {
		decision := r.Decision.clone()
		r.Decision = &decision
	}
	if r.Score != nil {
		score := *r.Score
		r.Score = &score
	}
	if r.Measurement != nil {
		measurement := *r.Measurement
		r.Measurement = &measurement
	}
	r.Metadata = r.Metadata.Clone()
	r.Details = slices.Clone(r.Details)
	for index := range r.Details {
		r.Details[index] = r.Details[index].cloneValid()
	}
	return r
}

// decisionRule identifies the rule behind a Report's verdict. A verdict that
// summarizes Details depends on their Metrics and rules, so they are part of
// its identity here; restating them in the summary's Parameters would give one
// rule two owners that a stored Report could make disagree.
type decisionRule struct {
	Metric     *Metric        `json:"metric,omitzero"`
	Policy     string         `json:"policy,omitempty"`
	Parameters metadata.Map   `json:"parameters,omitzero"`
	Details    []decisionRule `json:"details,omitempty"`
}

func (r Report) decisionRule() decisionRule {
	var rule decisionRule
	if r.Decision != nil {
		rule.Policy, rule.Parameters = r.Decision.Policy, r.Decision.Parameters
	}
	for _, detail := range r.Details {
		child := detail.decisionRule()
		child.Metric = &detail.Metric
		rule.Details = append(rule.Details, child)
	}
	return rule
}

// decisionIdentity is the canonical identity of the rule behind r's Decision.
func (r Report) decisionIdentity() (string, error) {
	if r.Decision == nil {
		return "", fmt.Errorf("%w: report has no decision", ErrInvalidReport)
	}
	if err := r.Validate(); err != nil {
		return "", err
	}
	return canonicalIdentity(r.decisionRule())
}

// DetailMetric is the Metric of one Report Detail with those of its own
// Details. A summary's Details define what it observed, so its Metric and its
// DetailMetrics together identify the observation; the summary Metric does not
// restate them.
type DetailMetric struct {
	Metric  Metric         `json:"metric"`
	Details []DetailMetric `json:"details,omitempty"`
}

func detailMetricsOf(details []Report) []DetailMetric {
	if len(details) == 0 {
		return nil
	}
	metrics := make([]DetailMetric, len(details))
	for index, detail := range details {
		metrics[index] = DetailMetric{Metric: detail.Metric, Details: detailMetricsOf(detail.Details)}
	}
	return metrics
}

// observationIdentity groups and pairs observations of the same Metric over
// the same Detail Metrics.
func observationIdentity(metric Metric, details []DetailMetric) (string, error) {
	identity, err := canonicalIdentity(DetailMetric{Metric: metric, Details: details})
	if err != nil {
		return "", fmt.Errorf("%w: encode identity: %w", ErrInvalidMetric, err)
	}
	return identity, nil
}

func canonicalIdentity(value any) (string, error) {
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		return "", err
	}
	canonical := jsontext.Value(encoded)
	if err := canonical.Format(jsontext.ReorderRawObjects(true)); err != nil {
		return "", err
	}
	return string(canonical), nil
}

func (r Report) Validate() error {
	return r.validate(1)
}

func (r Report) validate(depth int) error {
	if depth > MaxReportDepth {
		return fmt.Errorf("%w: detail depth exceeds %d", ErrInvalidReport, MaxReportDepth)
	}
	if err := r.Metric.Validate(); err != nil {
		return fmt.Errorf("%w: metric: %w", ErrInvalidReport, err)
	}
	if err := r.validateOutcomes(); err != nil {
		return err
	}
	if !utf8.ValidString(r.Feedback) {
		return fmt.Errorf("%w: feedback must be valid UTF-8", ErrInvalidReport)
	}
	if err := r.Metadata.Validate(); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrInvalidReport, err)
	}
	if !r.hasOutcome() {
		return fmt.Errorf("%w: at least one decision, score, measurement, feedback, or detail is required", ErrInvalidReport)
	}
	for index, detail := range r.Details {
		if err := detail.validate(depth + 1); err != nil {
			return fmt.Errorf("%w: details[%d]: %w", ErrInvalidReport, index, err)
		}
	}
	return nil
}

func (r Report) validateOutcomes() error {
	if r.Decision != nil {
		if err := r.Decision.Validate(); err != nil {
			return err
		}
	}
	if r.Score != nil {
		if err := r.Score.Validate(); err != nil {
			return fmt.Errorf("%w: score: %w", ErrInvalidReport, err)
		}
	}
	if r.Measurement != nil {
		value := *r.Measurement
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%w: measurement must be finite", ErrInvalidReport)
		}
	}
	return nil
}

func (r Report) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type wireReport Report
	return jsonv2.Marshal(wireReport(r))
}

func (r *Report) UnmarshalJSON(data []byte) error {
	if r == nil {
		return fmt.Errorf("%w: nil receiver", ErrInvalidReport)
	}
	type wireReport Report
	var decoded wireReport
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("%w: decode: %w", ErrInvalidReport, err)
	}
	candidate := Report(decoded)
	if err := candidate.Validate(); err != nil {
		return err
	}
	*r = candidate
	return nil
}

func (r Report) hasOutcome() bool {
	return r.Decision != nil || r.Score != nil || r.Measurement != nil ||
		strings.TrimSpace(r.Feedback) != "" || len(r.Details) > 0
}
