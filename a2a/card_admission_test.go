package a2a_test

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/Tangerg/scope/a2a"
)

func TestSendMessageEnforcesPublishedInputModes(t *testing.T) {
	for _, inherit := range []bool{false, true} {
		t.Run(fmt.Sprintf("inherits_default=%v", inherit), func(t *testing.T) {
			card := &sdk.AgentCard{Name: "echo", SupportedInterfaces: []*sdk.AgentInterface{sdk.NewAgentInterface("https://agent.example/rpc", sdk.TransportProtocolJSONRPC)}, DefaultInputModes: []string{"text/plain"}, Skills: []sdk.AgentSkill{{ID: "markdown", InputModes: []string{"text/markdown"}}}}
			if inherit {
				card.Skills = append(card.Skills, sdk.AgentSkill{ID: "default"})
			}
			handler, err := a2a.NewHTTPHandler(a2a.ServerConfig{Agent: echoAgent{}, TaskStore: testTaskStore(t), Card: card})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			// Neither publication nor admission should change when the caller reuses
			// its card after construction.
			card.Skills[0].InputModes[0] = "application/xml"
			card.DefaultInputModes[0] = "application/xml"
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+a2asrv.WellKnownAgentCardPath, nil)
			if err != nil {
				t.Fatal(err)
			}
			res, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var published sdk.AgentCard
			err = jsonv2.UnmarshalRead(res.Body, &published)
			_ = res.Body.Close()
			if err != nil || res.StatusCode != 200 || len(published.Skills) == 0 || published.Skills[0].InputModes[0] != "text/markdown" || published.DefaultInputModes[0] != "text/plain" {
				t.Fatalf("wrong published MIME declaration: %+v %v", published, err)
			}
			for _, tc := range []struct {
				mode    string
				allowed bool
			}{{"text/plain", inherit}, {"application/xml", false}} {
				body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"input","role":"ROLE_USER","parts":[{"text":"hello","mediaType":%q}]}}}`, tc.mode)
				rpc, reqErr := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/rpc", bytes.NewBufferString(body))
				if reqErr != nil {
					t.Fatal(reqErr)
				}
				rpc.Header.Set("Content-Type", "application/json")
				rpc.Header.Set(sdk.SvcParamVersion, string(sdk.Version))
				reply, callErr := server.Client().Do(rpc)
				if callErr != nil {
					t.Fatal(callErr)
				}
				encoded, readErr := io.ReadAll(reply.Body)
				_ = reply.Body.Close()
				if readErr != nil {
					t.Fatal(readErr)
				}
				var envelope struct {
					Result any `json:"result"`
					Error  *struct {
						Code int `json:"code"`
					} `json:"error"`
				}
				if err = jsonv2.Unmarshal(encoded, &envelope); err != nil {
					t.Fatal(err)
				}
				if tc.allowed {
					if envelope.Result == nil || envelope.Error != nil || !bytes.Contains(encoded, []byte("scope received: hello")) {
						t.Errorf("%s advertised input did not execute: %s", tc.mode, encoded)
					}
				} else if envelope.Result != nil || envelope.Error == nil || envelope.Error.Code != -32005 {
					t.Errorf("%s: want unsupported-content-type, got %s", tc.mode, encoded)
				}
			}
		})
	}
}

func TestInvalidDeclaredInputModeRefusesConstruction(t *testing.T) {
	card := &sdk.AgentCard{Name: "echo", SupportedInterfaces: []*sdk.AgentInterface{sdk.NewAgentInterface("https://agent.example/rpc", sdk.TransportProtocolJSONRPC)}, DefaultInputModes: []string{"not a MIME type"}}
	handler, err := a2a.NewHTTPHandler(a2a.ServerConfig{Card: card, Agent: echoAgent{}, TaskStore: testTaskStore(t)})
	if handler != nil || !errors.Is(err, a2a.ErrInvalidCard) || !errors.Is(err, sdk.ErrInvalidParams) {
		t.Fatalf("invalid published MIME: %v %v", handler, err)
	}
}
