package bedrock_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/models/bedrock"
)

func TestExplicitCredentialsOverrideAmbientBearer(t *testing.T) {
	const ambientToken = "ambient-tenant-token"
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", ambientToken)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	for _, explicit := range []bool{false, true} {
		for _, operation := range []string{"chat", "embedding"} {
			t.Run(fmt.Sprintf("%s/explicit=%t", operation, explicit), func(t *testing.T) {
				headers := make(chan http.Header, 1)
				server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					headers <- request.Header.Clone()
					if operation == "embedding" {
						writer.Header().Set("Content-Type", "application/json")
						_, _ = fmt.Fprint(writer, `{"embedding":[0.1,0.2],"inputTextTokenCount":2}`)
						return
					}
					writer.Header().Set("Content-Type", bedrockEventStreamContentType)
					writeBedrockEvent(writer, "messageStart", []byte(`{"role":"assistant"}`))
					writeBedrockEvent(writer, "contentBlockDelta", []byte(`{"contentBlockIndex":0,"delta":{"text":"ready"}}`))
					writeBedrockEvent(writer, "contentBlockStop", []byte(`{"contentBlockIndex":0}`))
					writeBedrockEvent(writer, "messageStop", []byte(`{"stopReason":"end_turn"}`))
					writeBedrockEvent(writer, "metadata", []byte(`{"usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`))
				}))
				defer server.Close()

				var credentials *bedrock.Credentials
				if explicit {
					credentials = &bedrock.Credentials{
						AccessKeyID: "explicit-tenant-id", SecretAccessKey: "explicit-tenant-secret", SessionToken: "explicit-session",
					}
				}
				if operation == "chat" {
					model, err := bedrock.NewChat(t.Context(), bedrock.ChatConfig{
						Region: "us-east-1", BaseURL: server.URL, HTTPClient: server.Client(), Credentials: credentials,
						DefaultOptions: chat.Options{Model: "anthropic.claude-test"},
					})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := model.Call(t.Context(), newBedrockBehaviorRequest(t)); err != nil {
						t.Fatal(err)
					}
				} else {
					model, err := bedrock.NewEmbeddingModel(t.Context(), bedrock.EmbeddingModelConfig{
						Region: "us-east-1", BaseURL: server.URL, HTTPClient: server.Client(), Credentials: credentials,
						DefaultOptions: embedding.Options{Model: "amazon.titan-embed-text-v2:0"},
					})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := model.Call(t.Context(), &embedding.Request{Texts: []string{"tenant input"}}); err != nil {
						t.Fatal(err)
					}
				}

				captured := <-headers
				authorization := captured.Get("Authorization")
				if explicit {
					if !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 ") || !strings.Contains(authorization, "Credential="+credentials.AccessKeyID+"/") {
						t.Errorf("Authorization = %q, want SigV4 with explicit identity", authorization)
					}
					if token := captured.Get("X-Amz-Security-Token"); token != credentials.SessionToken {
						t.Errorf("session token = %q, want explicit identity token", token)
					}
				} else if authorization != "Bearer "+ambientToken {
					t.Errorf("Authorization = %q, want ambient bearer", authorization)
				}
			})
		}
	}
}
