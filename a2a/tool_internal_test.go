package a2a

import (
	"errors"
	"testing"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	corechat "github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestNewToolValidatesPublicDefinition(t *testing.T) {
	_, err := newRemoteTool(remoteToolConfig{
		client: new(a2aclient.Client),
		card:   &sdka2a.AgentCard{Name: "Remote Agent"},
		name:   "invalid name",
	})
	if !errors.Is(err, corechat.ErrInvalidToolDefinition) {
		t.Fatalf("newRemoteTool error = %v, want chat.ErrInvalidToolDefinition", err)
	}
}

func TestRemoteToolRejectsBlankMessageAsDefiniteFailure(t *testing.T) {
	remote, err := newRemoteTool(remoteToolConfig{
		client: new(a2aclient.Client),
		card:   &sdka2a.AgentCard{Name: "Remote Agent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := toolcontract.Bind(remote)
	if err != nil {
		t.Fatal(err)
	}
	// A blank message passes schema admission but fails argument parsing before
	// the remote agent is called; the zero client is never reached.
	invocation, err := binding.Contract().Prepare(corechat.ToolCall{
		ID: "call-1", Name: binding.Contract().Definition().Name, Arguments: `{"message":"   "}`,
	})
	if err != nil {
		t.Fatalf("Prepare rejected blank message before Call: %v", err)
	}
	_, callErr := binding.Call(t.Context(), invocation)
	failure, ok := errors.AsType[*toolcontract.Failure](callErr)
	if !ok || failure.Kind() != toolcontract.FailureKindFailed {
		t.Fatalf("blank message call error = %v, want FailureKindFailed", callErr)
	}
}
