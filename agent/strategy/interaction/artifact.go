package interaction

import (
	"fmt"
	"slices"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

const maxCompletionFeedbackBytes = 4096

// Artifact is one successful, schema-validated Delegate output. It is
// identified by the exact Delegate binding, never by a Go runtime type name or
// an application artifact store.
type Artifact struct {
	modelCallSequence uint32
	toolCallID        string
	delegateName      string
	output            agent.Output
}

// DelegateName returns the exact model-facing Delegate name.
func (a Artifact) DelegateName() string { return a.delegateName }

// Output returns the immutable, schema-validated child output.
func (a Artifact) Output() agent.Output { return a.output }

// Decode strictly decodes a's output into T. The output was already
// validated against the exact Delegate Descriptor before the Artifact was
// admitted to Interaction state; T is only an edge convenience.
func (a Artifact) Decode[T any]() (T, error) {
	var zero T
	if !a.valid() {
		return zero, ErrInvalidArtifact
	}
	value, err := a.output.Decode[T]()
	if err != nil {
		return zero, fmt.Errorf("%w: decode: %w", ErrInvalidArtifact, err)
	}
	return value, nil
}

func (a Artifact) valid() bool {
	return a.modelCallSequence > 0 && a.toolCallID != "" &&
		a.delegateName != "" && a.output.Valid()
}

func newArtifacts(records []artifactRecord) []Artifact {
	values := make([]Artifact, len(records))
	for index, record := range records {
		values[index] = Artifact{
			modelCallSequence: record.ModelCallSequence, toolCallID: record.ToolCallID,
			delegateName: record.DelegateName, output: record.Output,
		}
	}
	return values
}

// CompletionCandidate is the immutable model context and semantic output
// proposed by an Interaction together with all successful Delegate Artifacts
// available at that boundary.
type CompletionCandidate struct {
	workingContext *chat.Request
	output         Output
	artifacts      []Artifact
}

// WorkingContext returns an independently owned copy of the model context
// preceding this candidate. It is Interaction state, not Host conversation or
// transcript history, and it does not yet contain the candidate Output.
func (c CompletionCandidate) WorkingContext() *chat.Request {
	return c.workingContext.Clone()
}

// Output returns an independently owned candidate Output.
func (c CompletionCandidate) Output() Output { return c.output.clone() }

// Artifacts returns an independently owned slice of successful Delegate outputs
// in original model ToolCall order across model calls. Each Artifact is immutable.
func (c CompletionCandidate) Artifacts() []Artifact { return slices.Clone(c.artifacts) }

// CompletionDecision is the explicit result of a CompletionValidator.
// Accepted=true requires empty Feedback. Accepted=false requires concise,
// non-empty Feedback that will be appended as a user message before the next
// model call.
type CompletionDecision struct {
	// Accepted permits completion with the proposed final semantic output.
	Accepted bool
	// Feedback explains a rejection to the model and is empty when accepted.
	Feedback string
}

func (c CompletionDecision) Valid() bool {
	if c.Accepted {
		return c.Feedback == ""
	}
	return c.Feedback != "" && strings.TrimSpace(c.Feedback) == c.Feedback &&
		len(c.Feedback) <= maxCompletionFeedbackBytes
}

// CompletionValidator decides whether a model or direct-Tool candidate is a
// valid semantic completion. It must be bounded, deterministic and
// side-effect-free: no I/O, clock, randomness, shared mutation or goroutines.
// A rejected candidate must return actionable Feedback; MaxModelCalls remains
// the hard bound on retry rounds. Evaluation requiring external work belongs
// in a managed child Process, not this callback.
type CompletionValidator func(candidate CompletionCandidate) (CompletionDecision, error)
