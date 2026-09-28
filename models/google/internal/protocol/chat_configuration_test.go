package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/chat"
)

func TestProtocolConfigRejectsCoreOwnedFields(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		for _, field := range []struct{ name, owner string }{
			{"temperature", "options.temperature"},
			{"topP", "options.top_p"},
			{"topK", "options.top_k"},
			{"maxOutputTokens", "options.max_output_tokens"},
			{"stopSequences", "options.stop"},
			{"presencePenalty", "options.presence_penalty"},
			{"frequencyPenalty", "options.frequency_penalty"},
			{"responseMimeType", "options.output_format"},
			{"responseSchema", "options.output_format"},
			{"responseJsonSchema", "options.output_format"},
			{"systemInstruction", "messages"},
		} {
			t.Run(provider+"/"+field.name, func(t *testing.T) {
				request := validReasoningRequest(t, "")
				if err := request.Options.Extensions.Set(protocolKey(provider, "request"), map[string]any{field.name: nil}); err != nil {
					t.Fatal(err)
				}
				_, _, _, err := mapProtocolRequest(provider, chat.Options{Model: "gemini"}, request)
				if err == nil || !strings.Contains(err.Error(), "owned by "+field.owner) {
					t.Fatalf("native field without a Core value bypassed ownership: %v", err)
				}
			})
		}
		for _, extension := range []struct{ name, value, owner string }{
			{"reasoning", `{"thinkingConfig":{"thinkingLevel":null}}`, "options.reasoning_effort"},
			{"tool choice", `{"toolConfig":{"functionCallingConfig":null}}`, "tool_choice"},
			{"functions", `{"tools":[{"googleSearch":{}},{"functionDeclarations":[]}]}`, "tools"},
		} {
			t.Run(provider+"/"+extension.name, func(t *testing.T) {
				request := validReasoningRequest(t, "")
				if err := request.Options.Extensions.Set(protocolKey(provider, "request"), json.RawMessage(extension.value)); err != nil {
					t.Fatal(err)
				}
				_, _, _, err := mapProtocolRequest(provider, chat.Options{Model: "gemini"}, request)
				if err == nil || !strings.Contains(err.Error(), "owned by "+extension.owner) {
					t.Fatalf("nested owner error = %v", err)
				}
			})
		}
	}
}

func TestProtocolConfigAcceptsOnlySDKFieldNames(t *testing.T) {
	for _, extension := range []string{
		`{"safety_settings":[]}`,
		`{"response_modalities":["TEXT"]}`,
		`{"thinking_config":{"includeThoughts":true}}`,
		`{"thinkingConfig":{"thinking_budget":100}}`,
		`{"tool_config":{}}`,
		`{"tools":[{"function_declarations":[]}]}`,
		`{"Temperature":0.5}`,
		`{"unknownOption":true}`,
	} {
		t.Run(extension, func(t *testing.T) {
			request := validReasoningRequest(t, "")
			if err := request.Options.Extensions.Set(RequestExtensionKey, json.RawMessage(extension)); err != nil {
				t.Fatal(err)
			}
			if _, _, _, err := mapProtocolRequest("google", chat.Options{Model: "gemini"}, request); err == nil {
				t.Fatal("accepted a second field spelling or an unknown option")
			}
		})
	}
}

func TestProtocolConfigPreservesNativeCapabilitiesAlongsideCore(t *testing.T) {
	for _, provider := range []string{"google", "vertexai"} {
		t.Run(provider, func(t *testing.T) {
			request, err := chat.NewRequest(
				chat.NewSystemMessage("system instruction"),
				chat.NewUserMessage(chat.NewTextPart("question")),
			)
			if err != nil {
				t.Fatal(err)
			}
			request.Tools = []chat.ToolDefinition{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}}
			request.ToolChoice = &chat.ToolChoice{Mode: chat.ToolChoiceNamed, Name: "lookup"}
			request.Options = chat.Options{Temperature: new(0.0), MaxOutputTokens: new(int64(64)), ReasoningEffort: "high", Stop: []string{"end"}}
			if extensionErr := request.Options.Extensions.Set(protocolKey(provider, "request"), json.RawMessage(`{
				"safetySettings":[{"category":"HARM_CATEGORY_HATE_SPEECH","threshold":"BLOCK_ONLY_HIGH"}],
				"responseModalities":["TEXT"],"thinkingConfig":{"includeThoughts":true},"tools":[{"googleSearch":{}}]
			}`)); extensionErr != nil {
				t.Fatal(extensionErr)
			}
			_, contents, config, err := mapProtocolRequest(provider, chat.Options{Model: "gemini"}, request)
			if err != nil {
				t.Fatal(err)
			}
			if config.Temperature == nil || *config.Temperature != 0 || config.MaxOutputTokens != 64 || len(config.StopSequences) != 1 || config.StopSequences[0] != "end" {
				t.Fatalf("Core generation options = %#v", config)
			}
			if len(contents) != 1 || config.SystemInstruction == nil || len(config.SystemInstruction.Parts) != 1 || config.SystemInstruction.Parts[0].Text != "system instruction" {
				t.Fatalf("Core messages = %#v, %#v", contents, config.SystemInstruction)
			}
			if len(config.Tools) != 2 || config.Tools[0].GoogleSearch == nil || len(config.Tools[0].FunctionDeclarations) != 0 || len(config.Tools[1].FunctionDeclarations) != 1 || config.Tools[1].FunctionDeclarations[0].Name != "lookup" {
				t.Fatalf("native and Core tools = %#v", config.Tools)
			}
			if config.ToolConfig == nil || config.ToolConfig.FunctionCallingConfig.Mode != genai.FunctionCallingConfigModeAny || len(config.ToolConfig.FunctionCallingConfig.AllowedFunctionNames) != 1 || config.ToolConfig.FunctionCallingConfig.AllowedFunctionNames[0] != "lookup" {
				t.Fatalf("Core tool choice = %#v", config.ToolConfig)
			}
			if config.ThinkingConfig == nil || !config.ThinkingConfig.IncludeThoughts || config.ThinkingConfig.ThinkingLevel != genai.ThinkingLevelHigh || len(config.ResponseModalities) != 1 || config.ResponseModalities[0] != "TEXT" || len(config.SafetySettings) != 1 || config.SafetySettings[0].Threshold != genai.HarmBlockThresholdBlockOnlyHigh {
				t.Fatalf("native configuration = %#v", config)
			}
		})
	}
}
