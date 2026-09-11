package a2a

import (
	"testing"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestToolConcurrencyPolicyDefaultsToExclusive(t *testing.T) {
	client := new(a2aclient.Client)
	tool, err := newRemoteTool(remoteToolConfig{
		client: client,
		card:   &sdka2a.AgentCard{Name: "Remote Agent"},
		name:   "remote_agent",
	})
	if err != nil {
		t.Fatalf("newRemoteTool: %v", err)
	}

	if tool.ConcurrencyPolicy() != nil {
		t.Fatal("default tool must not declare parallel execution")
	}
}

func TestToolConcurrencyPolicyUsesHostPolicy(t *testing.T) {
	var received toolcontract.Invocation
	remote, err := newRemoteTool(remoteToolConfig{
		client: new(a2aclient.Client),
		card:   &sdka2a.AgentCard{Name: "Remote Agent"},
		concurrencyPolicy: func(invocation toolcontract.Invocation) (string, bool) {
			received = invocation
			return "shared-account", true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := toolcontract.Bind(remote)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{
		ID: "call", Name: binding.Contract().Definition().Name, Arguments: `{"message":"write"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, concurrent := remote.ConcurrencyPolicy()(invocation)
	if key != "shared-account" || !concurrent || string(received.Arguments()) != `{"message":"write"}` {
		t.Fatalf("policy result = %q, %v; arguments = %s", key, concurrent, received.Arguments())
	}
}
