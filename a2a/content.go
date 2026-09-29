package a2a

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"strings"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
)

type textProjection struct{}

func (textProjection) userMessage(text string) *sdka2a.Message {
	return sdka2a.NewMessage(sdka2a.MessageRoleUser, sdka2a.NewTextPart(text))
}

// A2A has richer content than the text-only Agent boundary. Preserve text and
// JSON verbatim; represent binary or unsupported content with visible markers.
func (textProjection) parts(parts sdka2a.ContentParts) string {
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	for _, part := range parts {
		if part == nil {
			continue
		}
		switch content := part.Content.(type) {
		case sdka2a.Text:
			b.WriteString(string(content))
		case sdka2a.Data:
			// Decoded data holds Go maps; deterministic encoding keeps the
			// rendered text stable across identical requests.
			if raw, err := jsonv2.Marshal(content.Value, jsonv2.Deterministic(true)); err == nil {
				b.Write(raw)
			} else {
				b.WriteString("[unrenderable data]")
			}
		case sdka2a.URL:
			b.WriteString(string(content))
		case sdka2a.Raw:
			fmt.Fprintf(&b, "[binary content, %d bytes]", len(content))
		}
	}
	return b.String()
}

// Successful tasks prefer artifacts over their status message.
func (t textProjection) result(result sdka2a.SendMessageResult) (string, error) {
	switch r := result.(type) {
	case *sdka2a.Message:
		if r == nil {
			return "", fmt.Errorf("%w: nil message", ErrInvalidResult)
		}
		return t.parts(r.Parts), nil
	case *sdka2a.Task:
		if r == nil {
			return "", fmt.Errorf("%w: nil task", ErrInvalidResult)
		}
		if r.Status.State != sdka2a.TaskStateCompleted {
			return "", &RemoteAgentError{State: r.Status.State, Detail: t.status(r)}
		}
		return t.task(r), nil
	default:
		return "", fmt.Errorf("%w: unexpected %T", ErrInvalidResult, result)
	}
}

func (t textProjection) task(task *sdka2a.Task) string {
	if task == nil {
		return ""
	}
	var b strings.Builder
	for _, artifact := range task.Artifacts {
		if artifact != nil {
			b.WriteString(t.parts(artifact.Parts))
		}
	}
	if b.Len() == 0 {
		return t.status(task)
	}
	return b.String()
}

func (t textProjection) status(task *sdka2a.Task) string {
	if task == nil {
		return ""
	}
	if task.Status.Message == nil {
		return ""
	}
	return t.parts(task.Status.Message.Parts)
}
