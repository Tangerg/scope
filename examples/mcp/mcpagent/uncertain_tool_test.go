package main

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	scopemcp "github.com/Tangerg/scope/mcp"
)

func TestLostMCPResponsePreservesUnknownInteractionEffect(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	serverConn, clientConn := net.Pipe()
	serverTransport := &sdkmcp.IOTransport{Reader: serverConn, Writer: serverConn}
	clientTransport := &sdkmcp.IOTransport{Reader: clientConn, Writer: clientConn}
	executed := make(chan struct{})
	var effects atomic.Int32
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "uncertain-server"}, nil)
	server.AddTool(&sdkmcp.Tool{Name: "write", Description: "Perform a write.", InputSchema: json.RawMessage(`{"type":"object"}`)},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			effects.Add(1)
			close(executed)
			<-ctx.Done()
			return nil, ctx.Err()
		})
	ss, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "uncertain-client"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	available, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Name: "remote", Session: cs}}, scopemcp.ToolDiscoveryConfig{})
	if err != nil {
		t.Fatal(err)
	}
	toolSet, err := interaction.NewToolSet(interaction.ToolSetConfig{
		Name: "test.remote_tools", Description: "Remote tools.", Tools: available,
		ImplementationDigest: agent.ComputeDigest([]byte("remote-tools")), ConfigurationDigest: agent.ComputeDigest([]byte("remote-tools-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "test.remote_interaction", Description: "Retain uncertain writes.", MaxModelCalls: agent.NewQuota(2),
		Tools: toolSet, ToolBudget: agent.Budget{Steps: agent.NewQuota(8), Effects: agent.NewQuota(4), Signals: agent.NewQuota(8)},
	})
	if err != nil {
		t.Fatal(err)
	}
	var modelCalls atomic.Int32
	model := chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		modelCalls.Add(1)
		return &chat.Response{Output: &chat.Output{Message: new(chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "write_once", Name: available[0].Definition().Name, Arguments: `{}`}))), FinishReason: chat.FinishReasonToolCalls}}, nil
	})
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("interaction")), ConfigurationDigest: agent.ComputeDigest([]byte("interaction-config")),
	})
	if err != nil {
		t.Fatal(err)
	}
	observations := &agenttest.ObservationRecorder{}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter(), DeploymentResolver: deploymentResolver{toolSet.Deployment().DeploymentRef(): toolSet.Deployment()}, EventListeners: []agent.EventListener{observations}})
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close(context.WithoutCancel(ctx))
	input, err := agent.EncodePayload(interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("write once"))}})
	if err != nil {
		t.Fatal(err)
	}
	process, err := engine.Start(ctx, deployment, input)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Kill(context.WithoutCancel(ctx), "test cleanup")
	select {
	case <-executed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if closeErr := serverConn.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	event, err := observations.AwaitEvent(ctx, func(event agent.Event) bool {
		fact, ok := event.EffectFinished()
		return ok && fact.SettlementStatus() == agent.SettlementStatusUnknown
	})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := engine.CaptureTree(ctx, process.ID())
	if err != nil {
		t.Fatal(err)
	}
	effectID, _ := event.EffectID()
	found := false
	for _, snapshot := range tree.ProcessSnapshots() {
		for _, unknown := range snapshot.UnknownEffectIDs() {
			if unknown == effectID {
				found = true
			}
		}
	}
	if !found || effects.Load() != 1 || modelCalls.Load() != 1 {
		t.Fatalf("unknown retained=%t effects=%d model calls=%d", found, effects.Load(), modelCalls.Load())
	}
}
