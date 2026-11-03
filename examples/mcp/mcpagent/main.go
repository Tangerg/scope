package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"log"
	"strings"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/agent/interaction"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
)

type searchInput struct {
	Query string `json:"query" jsonschema:"required"`
}

type briefOutput struct {
	Sources   []string `json:"sources"`
	ToolError string   `json:"tool_error,omitzero"`
}

const (
	briefingBuild             = "example.mcp-briefing.v1"
	briefingRevision          = "mcp-briefing.v1"
	searchRevision            = "mcp-search.v1"
	briefingTopic             = "agent frameworks in 2026"
	briefingQuery             = "agent frameworks 2026"
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

	mcpClient := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "scope-mcp-agent"}, nil)
	clientSession, err := mcpClient.Connect(ctx, clientTransport, nil)
	if err != nil {
		return fmt.Errorf("connect MCP client: %w", err)
	}
	defer func() {
		if closeErr := clientSession.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close MCP client session: %w", closeErr))
		}
	}()

	systemPrompt, err := fetchSystemPrompt(ctx, clientSession, briefingTopic)
	if err != nil {
		return err
	}
	runtime, err := agentrun.New(ctx, agentrun.Config{Store: agentrun.NewMemoryStore(), Build: briefingBuild})
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := runtime.Close(context.WithoutCancel(ctx)); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close agent runtime: %w", closeErr))
		}
	}()
	program, err := newBriefingProgram(ctx, runtime.Compiler(), briefingConfig{Session: clientSession, ExpertRevision: "example.in-memory-research.v1", AllowSearch: true, Query: briefingQuery})
	if err != nil {
		return err
	}
	brief, err := runBriefing(ctx, runtime, program, systemPrompt, briefingTopic)
	if err != nil {
		return err
	}
	fmt.Println("\n--- result ---")
	fmt.Printf("topic:   %s\n", briefingTopic)
	fmt.Printf("sources: %v\n", brief.Sources)
	if brief.ToolError != "" {
		fmt.Printf("tool error: %s\n", brief.ToolError)
	}
	return nil
}

func fetchSystemPrompt(ctx context.Context, session *sdkmcp.ClientSession, topic string) (string, error) {
	promptResult, err := session.GetPrompt(ctx, &sdkmcp.GetPromptParams{
		Name: researchPromptName, Arguments: map[string]string{researchTopicArgument: topic},
	})
	if err != nil {
		return "", fmt.Errorf("get MCP prompt %q: %w", researchPromptName, err)
	}
	messages, err := scopemcp.PromptMessagesToChat(promptResult.Messages)
	if err != nil {
		return "", fmt.Errorf("convert MCP prompt messages: %w", err)
	}
	var systemPrompt strings.Builder
	for index := range messages {
		systemPrompt.WriteString(messages[index].Text())
	}
	return systemPrompt.String(), nil
}

type briefingConfig struct {
	Session        *sdkmcp.ClientSession
	ExpertRevision string
	AllowSearch    bool
	Query          string
}

type remoteToolIdentity interface {
	MCPToolIdentity() (sourceName, remoteName string)
}

func newBriefingProgram(ctx context.Context, compiler *agentrun.Compiler, config briefingConfig) (agentrun.Program[interaction.Input, interaction.Output], error) {
	available, err := scopemcp.DiscoverTools(ctx,
		[]scopemcp.ToolSource{{Name: researchToolSource, Session: config.Session}},
		scopemcp.ToolDiscoveryConfig{RequestMeta: func(context.Context, tool.Invocation) sdkmcp.Meta {
			return sdkmcp.Meta{requestMetaKey: json.RawMessage(`"mcp-agent"`)}
		}},
	)
	if err != nil {
		return agentrun.Program[interaction.Input, interaction.Output]{}, fmt.Errorf("discover MCP tools: %w", err)
	}
	var selected []tool.Tool
	for _, candidate := range available {
		identity, found := candidate.(remoteToolIdentity)
		if !found {
			return agentrun.Program[interaction.Input, interaction.Output]{}, errors.New("MCP tool has no remote identity")
		}
		source, remote := identity.MCPToolIdentity()
		if source == researchToolSource && remote == researchToolName {
			selected = append(selected, candidate)
		}
	}
	if len(selected) != 1 || config.Query == "" || config.ExpertRevision == "" {
		return agentrun.Program[interaction.Input, interaction.Output]{}, errors.New("briefing requires the selected search tool, expert identity, and an explicit query")
	}
	permit := config.AllowSearch
	guard, err := tool.NewGuard(tool.GuardConfig{
		Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) (bool, error) { return permit, nil }),
	})
	if err != nil {
		return agentrun.Program[interaction.Input, interaction.Output]{}, err
	}
	// Revision identifies the opaque authorization callback and remote artifact.
	// Compile owns hashing it together with the ToolSet's canonical contracts.
	toolRevision, err := jsonv2.Marshal(struct {
		Revision    string `json:"revision"`
		Expert      string `json:"expert"`
		AllowSearch bool   `json:"allow_search"`
	}{Revision: searchRevision, Expert: config.ExpertRevision, AllowSearch: permit}, jsonv2.Deterministic(true))
	if err != nil {
		return agentrun.Program[interaction.Input, interaction.Output]{}, err
	}
	toolSet, err := interaction.NewToolSet(interaction.ToolSetConfig{Revision: string(toolRevision), Tools: selected, Middlewares: []tool.CallMiddleware{guard.Call}})
	if err != nil {
		return agentrun.Program[interaction.Input, interaction.Output]{}, fmt.Errorf("create MCP tool set: %w", err)
	}
	chatClient, err := chatclient.New(&stubModel{query: config.Query})
	if err != nil {
		return agentrun.Program[interaction.Input, interaction.Output]{}, err
	}
	modelRevision, err := jsonv2.Marshal(struct {
		Revision string `json:"revision"`
		Query    string `json:"query"`
	}{Revision: briefingRevision, Query: config.Query}, jsonv2.Deterministic(true))
	if err != nil {
		return agentrun.Program[interaction.Input, interaction.Output]{}, err
	}
	return interaction.Compile(compiler, interaction.Config{
		Name: "example.mcp_briefing", Description: "Ask the model for a topic brief using a remote MCP search tool.",
		Revision: string(modelRevision), Model: interaction.CallModel(chatClient),
		MaxModelCalls: agentrun.NewQuota(briefingModelCallLimit), Tools: toolSet,
	})
}

func runBriefing(
	ctx context.Context,
	runtime *agentrun.Runtime,
	program agentrun.Program[interaction.Input, interaction.Output],
	systemPrompt, topic string,
) (briefOutput, error) {
	input, err := briefingInput(systemPrompt, topic)
	if err != nil {
		return briefOutput{}, err
	}
	handle, err := runtime.Start(ctx, program, agentrun.StartRequest[interaction.Input]{ID: agentrun.StartID{Scope: "mcp", Key: rand.Text()}, Input: input})
	if err != nil {
		return briefOutput{}, err
	}
	if _, err = handle.Await(ctx); err != nil {
		return briefOutput{}, err
	}
	result, err := handle.Await(ctx)
	if err != nil {
		return briefOutput{}, fmt.Errorf("run MCP briefing interaction: %w", err)
	}
	return decodeBriefing(result)
}

func briefingInput(systemPrompt, topic string) (interaction.Input, error) {
	outputFormat, err := chat.NewOutputFormat(chat.OutputFormatJSON)
	if err != nil {
		return interaction.Input{}, fmt.Errorf("create JSON output format: %w", err)
	}
	prompt := fmt.Sprintf("Use %s to gather source URLs on %q.", researchQualifiedToolName, topic)
	return interaction.Input{Messages: []chat.Message{
		chat.NewSystemMessage(systemPrompt),
		chat.NewUserMessage(chat.NewTextPart(prompt)),
	}, Options: chat.Options{OutputFormat: &outputFormat}}, nil
}

func decodeBriefing(result agentrun.Result[interaction.Output]) (briefOutput, error) {
	output, err := result.Output()
	if err != nil {
		return briefOutput{}, fmt.Errorf("MCP briefing: %w", err)
	}
	if output.ModelResponse == nil {
		return briefOutput{}, errors.New("MCP briefing completed with direct tool results instead of a model response")
	}
	return chatclient.JSON[briefOutput]().Decode(output.ModelResponse)
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

type stubModel struct{ query string }

func (s *stubModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			if part.Kind != chat.PartToolResult {
				continue
			}
			brief := briefOutput{Sources: []string{}}
			text, textOnly := part.ToolResult.Output.Text()
			if !textOnly {
				return nil, errors.New("search returned non-text content")
			}
			if part.ToolResult.IsError {
				brief.ToolError = text
			} else {
				var sources []struct {
					URL string `json:"url"`
				}
				if err := jsonv2.Unmarshal([]byte(text), &sources); err != nil {
					return nil, err
				}
				for _, source := range sources {
					brief.Sources = append(brief.Sources, source.URL)
				}
			}
			encoded, err := jsonv2.Marshal(brief)
			if err != nil {
				return nil, err
			}
			return responseWithText(string(encoded))
		}
	}
	arguments, err := jsonv2.Marshal(searchInput{Query: s.query})
	if err != nil {
		return nil, err
	}
	return responseWithToolCall(researchQualifiedToolName, string(arguments))
}

func responseWithText(text string) (*chat.Response, error) {
	message := chat.NewAssistantMessage(chat.NewTextPart(text))
	return chat.NewResponse(&chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}, nil)
}

func responseWithToolCall(name, arguments string) (*chat.Response, error) {
	message := chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: stubToolCallID, Name: name, Arguments: arguments}))
	return chat.NewResponse(&chat.Output{Message: &message, FinishReason: chat.FinishReasonToolCalls}, nil)
}
