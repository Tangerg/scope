package a2a

import (
	"errors"
	"testing"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/core/tool"
)

func TestTextProjectionRequiresSuccessfulResult(t *testing.T) {
	projection := textProjection{}
	detail := sdka2a.NewMessage(sdka2a.MessageRoleAgent, sdka2a.NewTextPart("more input needed"))
	unfinished := []sdka2a.TaskState{
		sdka2a.TaskStateUnspecified,
		sdka2a.TaskStateAuthRequired,
		sdka2a.TaskStateInputRequired,
		sdka2a.TaskStateSubmitted,
		sdka2a.TaskStateWorking,
	}
	for _, state := range unfinished {
		t.Run(string(state), func(t *testing.T) {
			_, err := projection.result(&sdka2a.Task{Status: sdka2a.TaskStatus{State: state, Message: detail}})
			remote, ok := errors.AsType[*RemoteAgentError](err)
			if !ok || remote.State != state || remote.Detail != "more input needed" {
				t.Fatalf("textProjection.result error = %#v, want RemoteAgentError for %q", err, state)
			}
			if _, definite := errors.AsType[*tool.Failure](err); definite {
				t.Fatalf("unfinished %q task became a definite failure", state)
			}
		})
	}

	for _, state := range []sdka2a.TaskState{sdka2a.TaskStateFailed, sdka2a.TaskStateCanceled, sdka2a.TaskStateRejected} {
		t.Run(string(state), func(t *testing.T) {
			_, err := projection.result(&sdka2a.Task{
				Status:    sdka2a.TaskStatus{State: state, Message: detail},
				Artifacts: []*sdka2a.Artifact{{Parts: sdka2a.ContentParts{sdka2a.NewTextPart("partial answer")}}},
			})
			failure, ok := errors.AsType[*tool.Failure](err)
			if !ok || failure.Kind() != tool.FailureKindFailed {
				t.Fatalf("ended %q task error = %#v, want a failed tool.Failure", state, err)
			}
			remote, ok := errors.AsType[*RemoteAgentError](failure.Cause())
			if !ok || remote.State != state || remote.Detail != "more input needed" {
				t.Fatalf("failure cause = %#v, want RemoteAgentError for %q", failure.Cause(), state)
			}
			want := remote.Error() + "\n\nOutput published before the task ended:\npartial answer"
			if text, ok := failure.Output().Text(); !ok || text != want {
				t.Fatalf("failure output = %q, want %q", text, want)
			}
		})
	}

	completed := &sdka2a.Task{
		Status: sdka2a.TaskStatus{State: sdka2a.TaskStateCompleted},
		Artifacts: []*sdka2a.Artifact{{
			Parts: sdka2a.ContentParts{sdka2a.NewTextPart("done")},
		}},
	}
	if text, err := projection.result(completed); err != nil || text != "done" {
		t.Fatalf("textProjection.result(completed) = %q, %v; want done, nil", text, err)
	}
}

func TestTextProjectionRejectsNilProtocolValues(t *testing.T) {
	projection := textProjection{}
	var message *sdka2a.Message
	var task *sdka2a.Task
	for _, result := range []sdka2a.SendMessageResult{nil, message, task} {
		if _, err := projection.result(result); !errors.Is(err, ErrInvalidResult) {
			t.Fatalf("textProjection.result(%T) error = %v, want ErrInvalidResult", result, err)
		}
	}
}

func TestRemoteAgentErrorNilReceiver(t *testing.T) {
	var remote *RemoteAgentError
	if got := remote.Error(); got != "a2a: remote agent task did not complete successfully" {
		t.Fatalf("nil RemoteAgentError = %q", got)
	}
}

func TestTextProjectionRendersDataDeterministically(t *testing.T) {
	value := make(map[string]any)
	want := "{"
	for key := 'a'; key <= 'z'; key++ {
		value[string(key)] = 1
		if key != 'a' {
			want += ","
		}
		want += `"` + string(key) + `":1`
	}
	want += "}"
	parts := sdka2a.ContentParts{sdka2a.NewDataPart(value)}
	for range 5 {
		if got := (textProjection{}).parts(parts); got != want {
			t.Fatalf("textProjection.parts(data) = %s, want %s", got, want)
		}
	}
}
