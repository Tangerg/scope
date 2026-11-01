package anthropic

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go/v2"

	corechat "github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestDialectPreparesResolvedRequestWithoutMutatingCaller(t *testing.T) {
	request := &corechat.Request{
		Messages:   []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))},
		Tools:      []toolcontract.Definition{{Name: "lookup", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: &corechat.ToolChoice{Mode: corechat.ToolChoiceAuto},
	}
	dialect := Dialect{Provider: "compatible", PrepareRequest: func(resolved *corechat.Request, fields map[string]any) error {
		if resolved.Options.ReasoningEffort != "high" || resolved.ToolChoice.Mode != corechat.ToolChoiceAuto {
			t.Fatalf("request policy did not receive effective options and tool choice: %#v", resolved)
		}
		resolved.Options.ReasoningEffort = ""
		resolved.ToolChoice.Mode = corechat.ToolChoiceNone
		resolved.Messages[0].Parts[0].Text = "prepared"
		fields["service_tier"] = "auto"
		return nil
	}}
	params, err := mapProtocolRequest(corechat.Options{Model: "compatible-model", ReasoningEffort: "high"}, request, dialect, false)
	if err != nil {
		t.Fatal(err)
	}
	if params.OutputConfig.Effort != "" || params.ToolChoice.OfNone == nil || params.Messages[0].OfUser.Content[0].OfText.Text != "prepared" {
		t.Fatalf("prepared request = %#v", params)
	}
	if request.Options.Model != "" || request.ToolChoice.Mode != corechat.ToolChoiceAuto || request.Messages[0].Parts[0].Text != "hello" {
		t.Fatalf("policy mutated caller request: %#v", request)
	}
}

func TestBindingOwnsCredentialAndWireRevision(t *testing.T) {
	for _, key := range []string{"Authorization", "x-api-key", "api-key", "anThRoPiC-VeRsIoN"} {
		config := apiConfig{APIKey: "native-key", BaseURL: "https://api.example.test", Headers: http.Header{key: []string{"other"}}}
		if _, _, err := config.requestOptions(t.Context()); err == nil {
			t.Fatalf("metadata overrode protected header %s", key)
		}
	}
	for _, item := range []struct {
		name   string
		header http.Header
		want   map[string]string
	}{
		{"official", nil, map[string]string{"Authorization": "Bearer key", "Anthropic-Version": "2026-10-21"}},
		{"compatible bearer", http.Header{"Anthropic-Version": []string{"2023-06-01"}, "X-Api-Key": nil, "Authorization": []string{"Bearer key"}}, map[string]string{"Authorization": "Bearer key", "Anthropic-Version": "2023-06-01"}},
		{"compatible x-api-key", http.Header{"Anthropic-Version": []string{"2023-06-01"}}, map[string]string{"X-Api-Key": "key", "Anthropic-Version": "2023-06-01"}},
		{"compatible api-key", http.Header{"Anthropic-Version": []string{"2023-06-01"}, "X-Api-Key": nil, "Api-Key": []string{"key"}}, map[string]string{"Api-Key": "key", "Anthropic-Version": "2023-06-01"}},
	} {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for _, key := range []string{"Authorization", "Api-Key", "X-Api-Key", "Anthropic-Version"} {
					if got := r.Header.Values(key); len(got) > 1 || r.Header.Get(key) != item.want[key] {
						t.Errorf("%s = %v, want %q", key, got, item.want[key])
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"input_tokens":1}`))
			}))
			defer server.Close()
			bound, err := newAPI(t.Context(), apiConfig{APIKey: "key", BaseURL: server.URL, HTTPClient: server.Client(), Headers: item.header})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = bound.countTokens(t.Context(), &anthropicsdk.MessageCountTokensParams{Model: "test", Messages: []anthropicsdk.MessageParam{anthropicsdk.NewUserMessage(anthropicsdk.NewTextBlock("x"))}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
