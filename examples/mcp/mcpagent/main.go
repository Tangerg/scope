package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/jsonschema"
	scopemcp "github.com/Tangerg/scope/mcp"
)

type searchInput struct {
	Query string `json:"query" jsonschema:"required"`
}

type briefOutput struct {
	Sources []string `json:"sources"`
}

const (
	briefingModelCallLimit    = 3
	researchToolSource        = "research"
	researchToolName          = "search"
	researchQualifiedToolName = researchToolSource + "_" + researchToolName
	researchPromptName        = "researcher_role"
	researchTopicArgument     = "topic"
	requestMetaKey            = "scope.example"
	mcpAssistantRole          = sdkmcp.Role("assistant")
	stubToolCallID            = "call_1"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) (err error) {
	model := &stubModel{}

	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	server, err := buildMCPServer()
	if err != nil {
		return err
	}
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return fmt.Errorf("connect MCP server: %w", err)
	}
	defer func() {
		if closeErr := serverSession.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close MCP server session: %w", closeErr))
		}
	}()

	mcpClient := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "scope-mcp-agent"},
		nil,
	)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		return fmt.Errorf("connect MCP client: %w", err)
	}
	defer func() {
		if closeErr := clientSession.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close MCP client session: %w", closeErr))
		}
	}()

	topic := "agent frameworks in 2026"
	promptResult, err := clientSession.GetPrompt(ctx, &sdkmcp.GetPromptParams{
		Name: researchPromptName, Arguments: map[string]string{researchTopicArgument: topic},
	})
	if err != nil {
		return fmt.Errorf("get MCP prompt %q: %w", researchPromptName, err)
	}
	systemMessages, err := scopemcp.PromptMessagesToChat(promptResult.Messages)
	if err != nil {
		return fmt.Errorf("convert MCP prompt messages: %w", err)
	}
	var systemPrompt strings.Builder
	for index := range systemMessages {
		systemPrompt.WriteString(systemMessages[index].Text())
	}
	availableTools, err := scopemcp.DiscoverTools(
		ctx,
		[]scopemcp.ToolSource{{Name: researchToolSource, Session: clientSession}},
		scopemcp.ToolDiscoveryConfig{RequestMeta: scopemcp.RequestMetaFromContext},
	)
	if err != nil {
		return fmt.Errorf("discover MCP tools: %w", err)
	}
	chatClient, err := chatclient.New(model, chatclient.Config{})
	if err != nil {
		return fmt.Errorf("create chat client: %w", err)
	}
	toolSet, err := interaction.NewToolSet(interaction.ToolSetConfig{
		Name: "example.mcp_tools", Description: "Execute each requested Tool through its own child Process.", Tools: availableTools,
		ImplementationDigest: agent.ComputeDigest([]byte("example.mcp_tools.implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("example.mcp_tools.configuration")),
	})
	if err != nil {
		return err
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name:          "example.mcp_briefing",
		Description:   "Ask the model for a topic brief using a remote MCP search tool.",
		MaxModelCalls: briefingModelCallLimit,
		Tools:         toolSet, ToolBudget: agent.Budget{Steps: 8, Effects: 4, Signals: 8},
	})
	if err != nil {
		return fmt.Errorf("create interaction definition: %w", err)
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{
		Model: chatClient,
	})
	if err != nil {
		return fmt.Errorf("create interaction dispatcher: %w", err)
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		Dispatcher:           dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("example-mcp-briefing-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("example-mcp-briefing-configuration")),
	})
	if err != nil {
		return fmt.Errorf("create agent deployment: %w", err)
	}
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: deploymentResolver{toolSet.Deployment().DeploymentRef(): toolSet.Deployment()}})
	if err != nil {
		return fmt.Errorf("create agent engine: %w", err)
	}
	defer func() {
		if closeErr := engine.Close(context.WithoutCancel(ctx)); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close agent engine: %w", closeErr))
		}
	}()

	outputFormat, err := chat.NewOutputFormat(chat.OutputFormatJSON)
	if err != nil {
		return fmt.Errorf("create JSON output format: %w", err)
	}
	prompt := fmt.Sprintf("Use %s to gather source URLs on %q.", researchQualifiedToolName, topic)
	input, err := agent.EncodeInput(interaction.Input{Messages: []chat.Message{
		chat.NewSystemMessage(systemPrompt.String()),
		chat.NewUserMessage(chat.NewTextPart(prompt)),
	}, Options: chat.Options{OutputFormat: &outputFormat}})
	if err != nil {
		return fmt.Errorf("encode interaction input: %w", err)
	}
	ctx = scopemcp.WithRequestMeta(ctx, sdkmcp.Meta{requestMetaKey: "mcp-agent"})
	result, err := engine.Run(ctx, deployment, input)
	if err != nil {
		return fmt.Errorf("run MCP briefing interaction: %w", err)
	}
	encodedOutput, ok := result.Output()
	if !ok {
		return fmt.Errorf("MCP briefing produced no output with status %q", result.Status())
	}
	output, err := encodedOutput.Decode[interaction.Output]()
	if err != nil {
		return fmt.Errorf("decode interaction output: %w", err)
	}
	if output.ModelResponse == nil {
		return fmt.Errorf("MCP briefing completed from source %q without a model response", output.Source)
	}
	var brief briefOutput
	if err := json.Unmarshal([]byte(output.ModelResponse.Text()), &brief); err != nil {
		return fmt.Errorf("decode model response as brief: %w", err)
	}

	fmt.Println("\n--- result ---")
	fmt.Printf("topic:   %s\n", topic)
	fmt.Printf("sources: %v\n", brief.Sources)
	return nil
}

func buildMCPServer() (*sdkmcp.Server, error) {
	inputSchema, err := jsonschema.For[searchInput]()
	if err != nil {
		return nil, fmt.Errorf("derive search tool input schema: %w", err)
	}
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: "research-server"},
		nil,
	)

	server.AddTool(
		&sdkmcp.Tool{
			Name:        researchToolName,
			Description: "search the public web for sources on a topic",
			InputSchema: inputSchema.JSON(),
		},
		func(_ context.Context, request *sdkmcp.CallToolRequest) (*sdkmcp.CallToolResult, error) {
			fmt.Printf("[mcp-server] tool call meta=%v\n", request.Params.Meta)
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{
					Text: `[{"url":"https://example.com/agents-2026","title":"Agents in 2026"}]`,
				}},
			}, nil
		},
	)

	server.AddPrompt(
		&sdkmcp.Prompt{
			Name:        researchPromptName,
			Description: "system prompt for a research analyst",
			Arguments: []*sdkmcp.PromptArgument{
				{Name: researchTopicArgument, Required: true},
			},
		},
		func(_ context.Context, request *sdkmcp.GetPromptRequest) (*sdkmcp.GetPromptResult, error) {
			topic := request.Params.Arguments[researchTopicArgument]
			return &sdkmcp.GetPromptResult{
				Messages: []*sdkmcp.PromptMessage{{
					Role: mcpAssistantRole,
					Content: &sdkmcp.TextContent{
						Text: fmt.Sprintf(
							"You are a research analyst focused on %q. Cite sources you used.",
							topic,
						),
					},
				}},
			}, nil
		},
	)

	return server, nil
}

type stubModel struct{}

func (s *stubModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	if !hasToolMessage(request.Messages) {
		return responseWithToolCall(researchQualifiedToolName, `{"query":"agent frameworks 2026"}`)
	}
	return responseWithText(`{"sources":["https://example.com/agents-2026"]}`)
}

func (s *stubModel) Stream(ctx context.Context, request *chat.Request) iter.Seq2[*chat.ResponseDelta, error] {
	response, err := s.Call(ctx, request)
	return func(yield func(*chat.ResponseDelta, error) bool) {
		if err != nil {
			yield(nil, err)
			return
		}
		delta := &chat.ResponseDelta{FinishReason: response.Output.FinishReason, Metadata: response.Metadata}
		if response.Output.Message != nil {
			for _, part := range response.Output.Message.Parts {
				switch part.Kind {
				case chat.PartText:
					delta.Parts = append(delta.Parts, chat.NewTextDelta(part.Text))
				case chat.PartToolCall:
					delta.Parts = append(delta.Parts, chat.NewToolCallDelta(chat.ToolCallDelta{
						ID: part.ToolCall.ID, Name: part.ToolCall.Name, Arguments: part.ToolCall.Arguments,
					}))
				}
			}
		}
		yield(delta, nil)
	}
}

func hasToolMessage(messages []chat.Message) bool {
	for _, message := range messages {
		if message.Role == chat.RoleTool {
			return true
		}
	}
	return false
}

func responseWithText(text string) (*chat.Response, error) {
	message := chat.NewAssistantMessage(chat.NewTextPart(text))
	return chat.NewResponse(&chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}, nil)
}

func responseWithToolCall(name, arguments string) (*chat.Response, error) {
	message := chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: stubToolCallID, Name: name, Arguments: arguments}))
	return chat.NewResponse(&chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}, nil)
}

type deploymentResolver map[agent.DeploymentRef]agent.Deployment

func (d deploymentResolver) Resolve(reference agent.DeploymentRef) (agent.Deployment, error) {
	deployment, found := d[reference]
	if !found {
		return agent.Deployment{}, fmt.Errorf("deployment %s is not bound", reference.Name())
	}
	return deployment, nil
}
