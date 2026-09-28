// Package text evaluates generated text without imposing one shared sample on
// metrics with different semantic inputs.
package text

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

var ErrInvalidSample = errors.New("eval/text: invalid sample")

const evidenceSeparator = "\n"

// AnswerRelevanceSample relates generated output to the input it should answer.
type AnswerRelevanceSample struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}

func (a AnswerRelevanceSample) Validate() error {
	if strings.TrimSpace(a.Input) == "" {
		return fmt.Errorf("%w: input is required", ErrInvalidSample)
	}
	if strings.TrimSpace(a.Output) == "" {
		return fmt.Errorf("%w: output is required", ErrInvalidSample)
	}
	return nil
}

// GroundednessSample keeps evidence separate from generated output so support
// is not conflated with answer relevance.
type GroundednessSample struct {
	Output   string   `json:"output"`
	Evidence []string `json:"evidence"`
}

func (g GroundednessSample) Clone() GroundednessSample {
	g.Evidence = slices.Clone(g.Evidence)
	return g
}

func (g GroundednessSample) EvidenceText() string {
	texts := make([]string, 0, len(g.Evidence))
	for _, text := range g.Evidence {
		if strings.TrimSpace(text) != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, evidenceSeparator)
}

func (g GroundednessSample) Validate() error {
	if strings.TrimSpace(g.Output) == "" {
		return fmt.Errorf("%w: output is required", ErrInvalidSample)
	}
	if g.EvidenceText() == "" {
		return fmt.Errorf("%w: evidence is required", ErrInvalidSample)
	}
	return nil
}

// CorrectnessSample supplies an explicit reference rather than treating
// retrieved evidence as ground truth.
type CorrectnessSample struct {
	Input     string `json:"input"`
	Output    string `json:"output"`
	Reference string `json:"reference"`
}

func (c CorrectnessSample) Validate() error {
	if strings.TrimSpace(c.Input) == "" {
		return fmt.Errorf("%w: input is required", ErrInvalidSample)
	}
	if strings.TrimSpace(c.Output) == "" {
		return fmt.Errorf("%w: output is required", ErrInvalidSample)
	}
	if strings.TrimSpace(c.Reference) == "" {
		return fmt.Errorf("%w: reference is required", ErrInvalidSample)
	}
	return nil
}
