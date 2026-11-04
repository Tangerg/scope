package a2a

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

const plainTextMediaType = "text/plain"

type textProjection struct{}

// requireTextExchange admits a remote card whose every skill can exchange plain
// text, the only mode this projection speaks; its other modes stay unused.
func (t textProjection) requireTextExchange(card *sdka2a.AgentCard) error {
	if len(card.Skills) == 0 {
		if err := t.requireTextMode(card.DefaultInputModes, card.DefaultOutputModes); err != nil {
			return fmt.Errorf("%w %q: default modes: %w", ErrInvalidCard, card.Name, err)
		}
	}
	for _, skill := range card.Skills {
		input, output := skill.InputModes, skill.OutputModes
		if len(input) == 0 {
			input = card.DefaultInputModes
		}
		if len(output) == 0 {
			output = card.DefaultOutputModes
		}
		if err := t.requireTextMode(input, output); err != nil {
			return fmt.Errorf("%w %q: skill %q: %w", ErrInvalidCard, card.Name, skill.ID, err)
		}
	}
	return nil
}

func (textProjection) requireTextMode(groups ...[]string) error {
	for _, modes := range groups {
		if len(modes) != 0 && !slices.Contains(modes, plainTextMediaType) {
			return fmt.Errorf("%w: declared modes %q exclude plain text", ErrUnsupportedContent, modes)
		}
	}
	return nil
}

func (t textProjection) validateCard(card *sdka2a.AgentCard) error {
	if err := t.validateModes(card.DefaultInputModes, card.DefaultOutputModes); err != nil {
		return fmt.Errorf("%w %q: default modes: %w", ErrInvalidCard, card.Name, err)
	}
	for _, skill := range card.Skills {
		if err := t.validateModes(skill.InputModes, skill.OutputModes); err != nil {
			return fmt.Errorf("%w %q: skill %q: %w", ErrInvalidCard, card.Name, skill.ID, err)
		}
	}
	return nil
}

func (textProjection) validateModes(groups ...[]string) error {
	for _, modes := range groups {
		for _, mode := range modes {
			if mode != plainTextMediaType {
				return fmt.Errorf("%w: mode %q cannot be projected as plain text", ErrUnsupportedContent, mode)
			}
		}
	}
	return nil
}

func (textProjection) userMessage(text string) *sdka2a.Message {
	return sdka2a.NewMessage(sdka2a.MessageRoleUser, sdka2a.NewTextPart(text))
}

// The text adapter accepts only plain text. A URL is not the referenced file,
// and a display marker cannot stand in for a binary artifact.
func (textProjection) parts(parts sdka2a.ContentParts) (string, error) {
	var out strings.Builder
	for index, part := range parts {
		if part == nil {
			return "", fmt.Errorf("%w: nil part %d", ErrInvalidResult, index)
		}
		text, ok := part.Content.(sdka2a.Text)
		if !ok || part.Filename != "" || len(part.Metadata) > 0 || (part.MediaType != "" && part.MediaType != plainTextMediaType) {
			return "", fmt.Errorf("%w at part %d (%T)", ErrUnsupportedContent, index, part.Content)
		}
		out.WriteString(string(text))
	}
	return out.String(), nil
}

// Successful tasks prefer artifacts over their status message. A task that ended
// unsuccessfully is a definite tool.Failure; any other unfinished state may
// still advance at the remote agent and stays an ordinary RemoteAgentError.
func (t textProjection) result(result sdka2a.SendMessageResult) (string, error) {
	switch r := result.(type) {
	case *sdka2a.Message:
		if r == nil {
			return "", fmt.Errorf("%w: nil message", ErrInvalidResult)
		}
		return t.parts(r.Parts)
	case *sdka2a.Task:
		if r == nil {
			return "", fmt.Errorf("%w: nil task", ErrInvalidResult)
		}
		switch r.Status.State {
		case sdka2a.TaskStateCompleted:
			return t.task(r)
		case sdka2a.TaskStateFailed, sdka2a.TaskStateCanceled, sdka2a.TaskStateRejected:
			return "", t.failure(r)
		default:
			detail, err := t.status(r)
			return "", errors.Join(&RemoteAgentError{State: r.Status.State, Detail: detail}, err)
		}
	default:
		return "", fmt.Errorf("%w: unexpected %T", ErrInvalidResult, result)
	}
}

// failure keeps the artifacts the task published before it ended, because the
// model can act on partial work only if it sees it.
func (t textProjection) failure(task *sdka2a.Task) error {
	detail, detailErr := t.status(task)
	cause := &RemoteAgentError{State: task.Status.State, Detail: detail}
	text := cause.Error()
	artifacts, artifactErr := t.artifacts(task)
	if artifacts != "" {
		text += "\n\nOutput published before the task ended:\n" + artifacts
	}
	if detailErr != nil || artifactErr != nil {
		text += "\nThe task returned content that the text adapter cannot represent."
	}
	failure, err := toolcontract.NewFailure(toolcontract.FailureConfig{
		Kind: toolcontract.FailureKindFailed, Cause: errors.Join(cause, detailErr, artifactErr), Output: toolcontract.NewTextOutput(text),
	})
	if err != nil {
		return errors.Join(cause, err)
	}
	return failure
}

func (t textProjection) task(task *sdka2a.Task) (string, error) {
	parts, err := t.artifacts(task)
	if err != nil || parts != "" {
		return parts, err
	}
	return t.status(task)
}
func (t textProjection) artifacts(task *sdka2a.Task) (string, error) {
	var b strings.Builder
	for _, a := range task.Artifacts {
		if a == nil {
			return "", fmt.Errorf("%w: nil artifact", ErrInvalidResult)
		}
		text, err := t.parts(a.Parts)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
	}
	return b.String(), nil
}
func (t textProjection) status(task *sdka2a.Task) (string, error) {
	if task.Status.Message == nil {
		return "", nil
	}
	return t.parts(task.Status.Message.Parts)
}
