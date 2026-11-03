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
	params, err := mapProtocolRequest(corechat.Options{Model: "compatible-model", ReasoningEffort: "high"}, request, dialect)
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
	for _, key := range []string{"Authorization", "x-api-key", "api-key", "anThRoPiC-VeRsIoN", "Cookie", "Proxy-Authorization"} {
		t.Run("reserved "+key, func(t *testing.T) {
			for _, values := range [][]string{nil, {"other"}, {"Bearer native-key"}} {
				config := apiConfig{APIKey: "native-key", BaseURL: "https://api.example.test", Revision: MessagesRevision20261021, APIKeyScheme: APIKeySchemeBearer, Headers: http.Header{key: values}}
				if _, err := config.requestOptions(t.Context()); err == nil {
					t.Fatalf("metadata overrode protected header %s", key)
				}
			}
		})
	}
	for _, item := range []struct {
		name       string
		revision   MessagesRevision
		scheme     APIKeyScheme
		wantHeader string
		wantValue  string
	}{
		{"official", MessagesRevision20261021, APIKeySchemeBearer, "Authorization", "Bearer key"},
		{"compatible bearer", MessagesRevision20230601, APIKeySchemeBearer, "Authorization", "Bearer key"},
		{"compatible x-api-key", MessagesRevision20230601, APIKeySchemeXAPIKey, "X-Api-Key", "key"},
		{"compatible api-key", MessagesRevision20230601, APIKeySchemeAPIKey, "Api-Key", "key"},
	} {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for _, key := range []string{"Authorization", "Api-Key", "X-Api-Key"} {
					want := ""
					if key == item.wantHeader {
						want = item.wantValue
					}
					if got := r.Header.Values(key); len(got) > 1 || r.Header.Get(key) != want {
						t.Errorf("%s = %v, want %q", key, got, want)
					}
				}
				if got := r.Header.Values("Anthropic-Version"); len(got) != 1 || got[0] != string(item.revision) {
					t.Errorf("version = %v", got)
				}
				if got := r.Header.Get("X-Attribution"); got != "application" {
					t.Errorf("attribution = %q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"input_tokens":1}`))
			}))
			defer server.Close()
			bound, err := newAPI(t.Context(), apiConfig{APIKey: "key", BaseURL: server.URL, HTTPClient: server.Client(), Revision: item.revision, APIKeyScheme: item.scheme, Headers: http.Header{"X-Attribution": {"application"}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = bound.countTokens(t.Context(), &anthropicsdk.MessageCountTokensParams{Model: "test", Messages: []anthropicsdk.MessageParam{anthropicsdk.NewUserMessage(anthropicsdk.NewTextBlock("x"))}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSamplingCapabilityIsIndependentOfWireRevision(t *testing.T) {
	for _, revision := range []MessagesRevision{MessagesRevision20230601, MessagesRevision20261021} {
		for _, supported := range []bool{false, true} {
			dialect := Dialect{Provider: "compatible", Revision: revision, APIKeyScheme: APIKeySchemeBearer, SupportsSampling: supported}
			request := &corechat.Request{Messages: []corechat.Message{corechat.NewUserMessage(corechat.NewTextPart("hello"))}, Options: corechat.Options{Temperature: new(0.5)}}
			_, err := mapProtocolRequest(corechat.Options{Model: "model"}, request, dialect)
			if (err == nil) != supported {
				t.Fatalf("revision=%s sampling=%v error=%v", revision, supported, err)
			}
		}
	}
}
