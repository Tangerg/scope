package generator

import (
	"slices"
	"testing"
)

func TestChatAdmissionUsesNativeTypeAndProtocol(t *testing.T) {
	for _, test := range []struct {
		name, provider string
		model          apiModel
		admitted       bool
	}{
		{"image with text output", "openai", apiModel{ID: "future-image", Type: modelTypeImage}, false},
		{"azure image", providerAzureOpenAI, apiModel{ID: "gpt-image-1.5", Type: modelTypeImage}, false},
		{"groq transcription", "groq", apiModel{ID: "whisper-large-v3", Type: modelTypeTranscription}, false},
		{"mistral transcription", "mistral", apiModel{ID: "voxtral-mini-latest", Type: modelTypeTranscription}, false},
		{"azure native chat", providerAzureOpenAI, apiModel{ID: "model", Provider: apiModelProvider{NPM: azureOpenAILibrary}}, true},
		{"azure compatible chat", providerAzureOpenAI, apiModel{ID: "model", Provider: apiModelProvider{NPM: openAICompatibleLibrary}}, true},
		{"azure unknown protocol", providerAzureOpenAI, apiModel{ID: "model", Provider: apiModelProvider{NPM: "@ai-sdk/unknown"}}, false},
		{"new azure messages model", providerAzureOpenAI, apiModel{ID: "new-model", Provider: apiModelProvider{NPM: "@ai-sdk/anthropic"}}, false},
		{"vertex native chat", providerVertexAI, apiModel{ID: "gemini-3.8-flash", Provider: apiModelProvider{NPM: googleVertexLibrary}}, true},
		{"vertex foreign API override", providerVertexAI, apiModel{ID: "model", Provider: apiModelProvider{NPM: googleVertexLibrary, API: "https://other.example/v1"}}, false},
		{"vertex messages", providerVertexAI, apiModel{ID: "claude-fable-5", Provider: apiModelProvider{NPM: "@ai-sdk/google-vertex/anthropic"}}, false},
		{"vertex unknown protocol", providerVertexAI, apiModel{ID: "publisher/model", Provider: apiModelProvider{NPM: "@ai-sdk/unknown"}}, false},
		{"historical vertex messages", providerVertexAI, apiModel{ID: "claude-opus-4@20250514", Provider: apiModelProvider{NPM: "@ai-sdk/google-vertex/anthropic"}, Status: statusDeprecated}, true},
		{"interactions agent", providerGoogle, apiModel{ID: "deep-research-preview-04-2026"}, false},
		{"gemini image conversation", providerGoogle, apiModel{ID: "gemini-image", Type: modelTypeImage}, true},
		{"openrouter image conversation", providerOpenRouter, apiModel{ID: "vendor/image", Type: modelTypeImage}, true},
		{"alibaba ASR conversation", providerAlibaba, apiModel{ID: "qwen3-asr-flash", Type: modelTypeTranscription}, true},
		{"vertex MaaS conversation", providerVertexAI, apiModel{ID: "meta/llama-4-maverick-17b-128e-instruct-maas", Provider: apiModelProvider{NPM: openAICompatibleLibrary, API: vertexOpenMaaSAPI}}, true},
		{"vertex OpenAPI does not prove gpt-oss GenerateContent", providerVertexAI, apiModel{ID: "openai/gpt-oss-120b-maas", Provider: apiModelProvider{NPM: openAICompatibleLibrary, API: vertexOpenMaaSAPI}}, false},
		{"vertex OpenAPI does not prove Grok GenerateContent", providerVertexAI, apiModel{ID: "xai/grok-4.3", Provider: apiModelProvider{NPM: openAICompatibleLibrary, API: vertexOpenMaaSAPI}}, false},
		{"vertex unqualified MaaS", providerVertexAI, apiModel{ID: "llama-4-maverick-17b-128e-instruct-maas", Provider: apiModelProvider{NPM: openAICompatibleLibrary, API: vertexOpenMaaSAPI}}, false},
		{"vertex absent publisher", providerVertexAI, apiModel{ID: "/model", Provider: apiModelProvider{NPM: openAICompatibleLibrary, API: vertexOpenMaaSAPI}}, false},
		{"vertex absent model", providerVertexAI, apiModel{ID: "publisher/", Provider: apiModelProvider{NPM: openAICompatibleLibrary, API: vertexOpenMaaSAPI}}, false},
		{"vertex noncanonical MaaS identity", providerVertexAI, apiModel{ID: "publisher/model/other", Provider: apiModelProvider{NPM: openAICompatibleLibrary, API: vertexOpenMaaSAPI}}, false},
		{"vertex foreign compatible endpoint", providerVertexAI, apiModel{ID: "publisher/model", Provider: apiModelProvider{NPM: openAICompatibleLibrary, API: "https://other.example/v1"}}, false},
		{"ordinary chat", "openai", apiModel{ID: "model", Type: modelTypeChat}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.model.Modalities.Output = []string{"text"}
			if got := test.model.supportsChat(test.provider); got != test.admitted {
				t.Fatalf("chat admission = %t, want %t", got, test.admitted)
			}
		})
	}
}

func TestGenerateOmitsForeignProtocolBeforePublishing(t *testing.T) {
	fixture := newGeneratorFixture(t)
	fixture.api["azure"].Models["foreign"] = apiModel{ID: "foreign", Provider: apiModelProvider{NPM: "@ai-sdk/anthropic"}}
	fixture.api["groq"].Models["whisper"] = apiModel{ID: "whisper", Type: modelTypeTranscription}
	fixture.api["google"].Models["deep-research-next"] = apiModel{ID: "deep-research-next"}
	for _, id := range []string{"codestral-2", "mistral-small-2503", "mistral-medium-3", "mistralai/codestral-2"} {
		fixture.api["google-vertex"].Models[id] = apiModel{ID: id, Provider: apiModelProvider{
			NPM: "@ai-sdk/mistral",
			API: "https://${GOOGLE_VERTEX_ENDPOINT}/v1/projects/${GOOGLE_VERTEX_PROJECT}/locations/${GOOGLE_VERTEX_LOCATION}/publishers/mistralai/models",
		}}
	}
	if err := Generate(t.Context(), fixture.write(t)); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{providerAzureOpenAI, "groq", providerGoogle, providerVertexAI} {
		published := fixture.provider(t, provider)
		if len(published.Models) != 1 || published.Models[0].ID != "model" {
			t.Fatalf("%s published incompatible binding: %#v", provider, published.Models)
		}
	}
}

func TestGeneratePreservesQualifiedVertexMaaSAndHistoricalModels(t *testing.T) {
	fixture := newGeneratorFixture(t)
	models := fixture.api["google-vertex"].Models
	want := []string{"model", "claude-opus-4@20250514", "meta/llama-4-maverick-17b-128e-instruct-maas"}
	models["meta/llama-4-maverick-17b-128e-instruct-maas"] = apiModel{ID: "meta/llama-4-maverick-17b-128e-instruct-maas", Provider: apiModelProvider{
		NPM: "@ai-sdk/openai-compatible",
		API: "https://${GOOGLE_VERTEX_ENDPOINT}/v1/projects/${GOOGLE_VERTEX_PROJECT}/locations/${GOOGLE_VERTEX_LOCATION}/endpoints/openapi",
	}}
	models["claude-opus-4@20250514"] = apiModel{ID: "claude-opus-4@20250514", Status: statusDeprecated, Provider: apiModelProvider{NPM: "@ai-sdk/google-vertex/anthropic"}}
	if err := Generate(t.Context(), fixture.write(t)); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, model := range fixture.provider(t, providerVertexAI).Models {
		got = append(got, model.ID)
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("native and historical identities changed: got %v, want %v", got, want)
	}
}

func TestGenerateResolvesEachModelsNativeBindingFromItsSource(t *testing.T) {
	fixture := newGeneratorFixture(t)
	source := fixture.api["google-vertex"]
	source.NPM = "@ai-sdk/unknown"
	source.Models["native"] = apiModel{ID: "native", Provider: apiModelProvider{NPM: googleVertexLibrary}}
	fixture.api["google-vertex"] = source
	if err := Generate(t.Context(), fixture.write(t)); err != nil {
		t.Fatal(err)
	}
	models := fixture.provider(t, providerVertexAI).Models
	if len(models) != 1 || models[0].ID != "native" {
		t.Fatalf("models ignored their effective source binding: %+v", models)
	}
}
