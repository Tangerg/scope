package eval

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/Tangerg/scope/core/metadata"
)

// MaxReportDepth bounds recursive detail trees at every public trust boundary.
const MaxReportDepth = 64

// Report is one evaluation result. Verdict, normalized Score, and raw
// Measurement are independent and optional so measurement-only and qualitative
// evaluations do not need to invent a pass threshold or quality score. Details
// contains owned child reports instead of convention-based metadata keys.
type Report struct {
	Metric      Metric       `json:"metric"`
	Verdict     Verdict      `json:"verdict,omitzero"`
	Score       *Score       `json:"score,omitzero"`
	Measurement *float64     `json:"measurement,omitzero"`
	Feedback    string       `json:"feedback,omitzero"`
	Metadata    metadata.Map `json:"metadata,omitzero"`
	Details     []Report     `json:"details,omitzero"`
}

// Clone validates the complete detail tree before allocating its detached copy.
func (r Report) Clone() (Report, error) {
	if err := r.Validate(); err != nil {
		return Report{}, err
	}
	return r.cloneValid(), nil
}

func (r Report) cloneValid() Report {
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
	if err := r.Verdict.Validate(); err != nil {
		return err
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
	if err := r.Metadata.Validate(); err != nil {
		return fmt.Errorf("%w: metadata: %w", ErrInvalidReport, err)
	}
	if !r.hasOutcome() {
		return fmt.Errorf("%w: at least one verdict, score, measurement, feedback, or detail is required", ErrInvalidReport)
	}
	for index, detail := range r.Details {
		if err := detail.validate(depth + 1); err != nil {
			return fmt.Errorf("%w: details[%d]: %w", ErrInvalidReport, index, err)
		}
	}
	return nil
}

func (r Report) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type wireReport Report
	return json.Marshal(wireReport(r))
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
	return r.Verdict.Decided() || r.Score != nil || r.Measurement != nil ||
		strings.TrimSpace(r.Feedback) != "" || len(r.Details) > 0
}
