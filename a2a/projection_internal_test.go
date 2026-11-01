package a2a

import (
	"errors"
	"strings"
	"testing"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
)

func requireText(t *testing.T, parts sdka2a.ContentParts) string {
	t.Helper()
	s, err := (textProjection{}).parts(parts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestTextBoundaryRejectsLossyConversions(t *testing.T) {
	for _, part := range []*sdka2a.Part{sdka2a.NewDataPart(map[string]any{"cost": 42}), sdka2a.NewFileURLPart("https://example.com/a", "image/png"), sdka2a.NewRawPart([]byte{0xff}), {Content: sdka2a.Text("body"), Filename: "header"}} {
		parts := sdka2a.ContentParts{sdka2a.NewTextPart("prefix"), part}
		if got, err := (textProjection{}).parts(parts); got != "" || !errors.Is(err, ErrUnsupportedContent) {
			t.Fatalf("lossy projection: %q %v", got, err)
		}
	}
	if _, err := (textProjection{}).parts(sdka2a.ContentParts{nil}); !errors.Is(err, ErrInvalidResult) {
		t.Fatal(err)
	}
	if text := requireText(t, sdka2a.ContentParts{sdka2a.NewTextPart("first "), sdka2a.NewTextPart("second")}); text != "first second" {
		t.Fatal(text)
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
