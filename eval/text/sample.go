// Package text evaluates generated text without imposing one shared sample on
// metrics with different semantic inputs.
package text

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

var ErrInvalidSample = errors.New("eval/text: invalid sample")

const evidenceSeparator = "\n"

// AnswerRelevanceSample relates generated output to the input it should answer.
type AnswerRelevanceSample struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}

func (a AnswerRelevanceSample) Validate() error {
	if err := validateRequiredText("input", a.Input); err != nil {
		return err
	}
	return validateRequiredText("output", a.Output)
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
	if err := validateRequiredText("output", g.Output); err != nil {
		return err
	}
	for index, evidence := range g.Evidence {
		if !utf8.ValidString(evidence) {
			return fmt.Errorf("%w: evidence[%d] must be valid UTF-8", ErrInvalidSample, index)
		}
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
	if err := validateRequiredText("input", c.Input); err != nil {
		return err
	}
	if err := validateRequiredText("output", c.Output); err != nil {
		return err
	}
	return validateRequiredText("reference", c.Reference)
}

func validateRequiredText(label, value string) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s must be valid UTF-8", ErrInvalidSample, label)
	}
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidSample, label)
	}
	return nil
}
