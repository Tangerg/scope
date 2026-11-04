package a2a

import (
	"errors"
	"testing"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestNewToolValidatesPublicDefinition(t *testing.T) {
	_, err := newRemoteTool(remoteToolConfig{
		client: new(a2aclient.Client),
		card:   &sdka2a.AgentCard{Name: "Remote Agent"},
		name:   "invalid name",
	})
	if !errors.Is(err, toolcontract.ErrInvalidDefinition) {
		t.Fatalf("newRemoteTool error = %v, want chat.ErrInvalidToolDefinition", err)
	}
}

func TestInvalidEffectIsRejectedBeforeEndpointDiscovery(t *testing.T) {
	_, err := OpenToolSet(t.Context(), Endpoint{Effect: toolcontract.Effect("invalid")})
	if !errors.Is(err, toolcontract.ErrInvalidDefinition) {
		t.Fatalf("invalid effect reached card resolution: %v", err)
	}
}

func TestRemoteToolRejectsBlankMessageDuringAdmission(t *testing.T) {
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
	for _, arguments := range []string{`{"message":""}`, `{"message":"   "}`, `{"message":"\t\n"}`} {
		_, err := binding.Contract().Prepare(toolcontract.Request{Name: binding.Contract().Name(), Arguments: arguments}, toolcontract.Admission{})
		if !errors.Is(err, toolcontract.ErrInvalidInvocation) {
			t.Fatalf("Prepare(%s) = %v, want ErrInvalidInvocation", arguments, err)
		}
		if rejection, public := toolcontract.ClassifyInputRejection(err); !public || rejection.Message() == "" || rejection.Cause() == nil {
			t.Fatalf("Prepare(%s) did not expose a correctable input rejection: %v", arguments, err)
		}
	}
}

func TestUnprojectableRemoteQuestionsRemainUnanswered(t *testing.T) {
	r := remoteTool{}
	for _, tc := range []struct {
		task  *sdka2a.Task
		cause error
	}{
		{&sdka2a.Task{ID: "task", ContextID: "ctx"}, ErrInvalidResult},
		{&sdka2a.Task{ID: "task", ContextID: "ctx", Status: sdka2a.TaskStatus{Message: sdka2a.NewMessage(sdka2a.MessageRoleAgent, sdka2a.NewRawPart([]byte{0xff}))}}, ErrUnsupportedContent},
		{&sdka2a.Task{ID: "task", ContextID: "ctx", Status: sdka2a.TaskStatus{Message: sdka2a.NewMessage(sdka2a.MessageRoleAgent, sdka2a.NewTextPart(" "))}}, ErrInvalidResult},
	} {
		res, err := r.awaitInput(tc.task, toolcontract.Invocation{})
		if res.InputRequest != nil || !errors.Is(err, tc.cause) {
			t.Fatalf("invalid question escaped: %+v %v", res, err)
		}
	}
}
