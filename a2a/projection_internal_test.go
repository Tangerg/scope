package a2a

import (
	"strings"
	"testing"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
)

func TestTextProjectionRendersEveryContentKind(t *testing.T) {
	cases := map[string]struct {
		parts    sdka2a.ContentParts
		contains string
	}{
		"text": {
			parts:    sdka2a.ContentParts{sdka2a.NewTextPart("hello")},
			contains: "hello",
		},
		"structured data": {
			parts:    sdka2a.ContentParts{sdka2a.NewDataPart(map[string]any{"answer": 42})},
			contains: `"answer":42`,
		},
		"url": {
			parts:    sdka2a.ContentParts{sdka2a.NewFileURLPart("https://example.com/a", "image/png")},
			contains: "https://example.com/a",
		},
		"several parts concatenate": {
			parts: sdka2a.ContentParts{
				sdka2a.NewTextPart("first "),
				sdka2a.NewTextPart("second"),
			},
			contains: "first second",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			rendered := textProjection{}.parts(testCase.parts)
			if !strings.Contains(rendered, testCase.contains) {
				t.Fatalf("rendered %q, want it to contain %q", rendered, testCase.contains)
			}
		})
	}
}

func TestTextProjectionSkipsNothingSilently(t *testing.T) {
	if rendered := (textProjection{}).parts(nil); rendered != "" {
		t.Fatalf("empty parts rendered %q", rendered)
	}

	withNil := sdka2a.ContentParts{nil, sdka2a.NewTextPart("kept")}
	if rendered := (textProjection{}).parts(withNil); rendered != "kept" {
		t.Fatalf("a nil part changed the rendering: %q", rendered)
	}

	unrenderable := sdka2a.ContentParts{sdka2a.NewDataPart(make(chan int))}
	rendered := textProjection{}.parts(unrenderable)
	if rendered == "" {
		t.Fatal("an unrenderable payload vanished instead of leaving a marker")
	}
}

func TestRemoteAgentErrorDistinguishesRemoteFailureFromTransport(t *testing.T) {
	withDetail := &RemoteAgentError{State: sdka2a.TaskStateFailed, Detail: "model refused"}
	message := withDetail.Error()
	if !strings.Contains(message, "model refused") || !strings.Contains(message, string(sdka2a.TaskStateFailed)) {
		t.Fatalf("error = %q, want it to carry both the state and the detail", message)
	}

	withoutDetail := &RemoteAgentError{State: sdka2a.TaskStateCanceled}
	message = withoutDetail.Error()
	if strings.Contains(message, ":") && strings.HasSuffix(message, ": ") {
		t.Fatalf("error = %q, want no dangling detail separator", message)
	}
	if !strings.Contains(message, string(sdka2a.TaskStateCanceled)) {
		t.Fatalf("error = %q, want it to carry the state", message)
	}

	var nilError *RemoteAgentError
	if nilError.Error() == "" {
		t.Fatal("a nil RemoteAgentError produced no message")
	}
}
