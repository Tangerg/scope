package a2a

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"strings"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
)

// textProjection owns the package's fixed, text-first projection between A2A
// protocol content and the scope tool/agent boundaries. Its zero value is ready
// for use.
type textProjection struct{}

func (textProjection) userMessage(text string) *sdka2a.Message {
	return sdka2a.NewMessage(sdka2a.MessageRoleUser, sdka2a.NewTextPart(text))
}

// parts renders A2A content parts to a single string: text parts are
// concatenated verbatim, structured data parts are JSON-encoded, and other
// kinds (raw bytes, file URLs) are described compactly. tools and the
// chat loop are text-first, so this is the lossy-but-faithful projection.
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
			if raw, err := jsonv2.Marshal(content.Value); err == nil {
				b.Write(raw)
			} else {
				// Don't let the part vanish silently — leave a marker so the
				// reader knows something was here.
				b.WriteString("[unrenderable data]")
			}
		case sdka2a.URL:
			b.WriteString(string(content))
		case sdka2a.Raw:
			// Binary payloads have no faithful text form; note the size.
			fmt.Fprintf(&b, "[binary content, %d bytes]", len(content))
		}
	}
	return b.String()
}

// result extracts the reply text from a SendMessageResult and reports a
// *RemoteAgentError unless a returned task completed successfully. A direct
// Message reply yields its parts; a completed Task reply prefers its artifacts,
// falling back to the status message.
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

// task concatenates a task's artifact parts, falling back to its status
// message when no artifacts are present.
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
