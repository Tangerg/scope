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
			card := &sdk.AgentCard{Name: "echo", SupportedInterfaces: []*sdk.AgentInterface{sdk.NewAgentInterface("https://agent.example/rpc", sdk.TransportProtocolJSONRPC)}, DefaultInputModes: []string{"text/plain"}, Skills: []sdk.AgentSkill{{ID: "echo", InputModes: []string{"text/plain"}}}}
			if inherit {
				card.Skills[0].InputModes = nil
			}
			handler, err := a2a.NewHTTPHandler(a2a.ServerConfig{Agent: echoAgent{}, TaskStore: testTaskStore(t), Card: card})
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(handler)
			defer server.Close()
			// Neither publication nor admission should change when the caller reuses
			// its card after construction.
			card.Skills[0].InputModes = []string{"application/xml"}
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
			if err != nil || res.StatusCode != http.StatusOK || len(published.Skills) == 0 || published.DefaultInputModes[0] != "text/plain" {
				t.Fatalf("wrong published MIME declaration: %+v %v", published, err)
			}
			if modes := published.Skills[0].InputModes; inherit && len(modes) != 0 || !inherit && (len(modes) != 1 || modes[0] != "text/plain") {
				t.Fatalf("published skill modes = %v, inherit = %v", modes, inherit)
			}
			for _, tc := range []struct {
				mode    string
				allowed bool
			}{{"text/plain", true}, {"text/markdown", false}, {"application/xml", false}} {
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

func TestTextHandlerRejectsUnrepresentableDeclaredModes(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*sdk.AgentCard)
	}{
		{name: "default input", configure: func(card *sdk.AgentCard) { card.DefaultInputModes = []string{"text/markdown"} }},
		{name: "default output", configure: func(card *sdk.AgentCard) { card.DefaultOutputModes = []string{"application/json"} }},
		{name: "skill input", configure: func(card *sdk.AgentCard) {
			card.Skills = []sdk.AgentSkill{{ID: "render", InputModes: []string{"text/markdown"}}}
		}},
		{name: "skill output", configure: func(card *sdk.AgentCard) {
			card.Skills = []sdk.AgentSkill{{ID: "render", OutputModes: []string{"image/png"}}}
		}},
		{name: "wildcard", configure: func(card *sdk.AgentCard) { card.DefaultInputModes = []string{"text/*"} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			card := &sdk.AgentCard{
				Name:                "echo",
				SupportedInterfaces: []*sdk.AgentInterface{sdk.NewAgentInterface("https://agent.example/rpc", sdk.TransportProtocolJSONRPC)},
				DefaultInputModes:   []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"},
			}
			test.configure(card)
			handler, err := a2a.NewHTTPHandler(a2a.ServerConfig{Agent: echoAgent{}, Card: card, TaskStore: testTaskStore(t)})
			if handler != nil || !errors.Is(err, a2a.ErrInvalidCard) || !errors.Is(err, a2a.ErrUnsupportedContent) {
				t.Fatalf("unrepresentable card = %v, %v", handler, err)
			}
		})
	}
}

func TestRemoteTextAdmissionUsesEffectiveSkillModes(t *testing.T) {
	for _, test := range []struct {
		name      string
		configure func(*sdk.AgentCard)
		valid     bool
	}{
		{name: "unspecified modes", configure: func(*sdk.AgentCard) {}, valid: true},
		{name: "multimodal subset", configure: func(card *sdk.AgentCard) {
			card.DefaultInputModes = []string{"text/plain", "image/png"}
			card.DefaultOutputModes = []string{"text/plain", "application/json"}
		}, valid: true},
		{name: "inherited compatible modes", configure: func(card *sdk.AgentCard) {
			card.DefaultInputModes, card.DefaultOutputModes = []string{"text/plain"}, []string{"text/plain"}
			card.Skills = []sdk.AgentSkill{{ID: "read"}}
		}, valid: true},
		{name: "inherited incompatible modes", configure: func(card *sdk.AgentCard) {
			card.DefaultOutputModes = []string{"image/png"}
			card.Skills = []sdk.AgentSkill{{ID: "render"}}
		}},
		{name: "skill overrides unused defaults", configure: func(card *sdk.AgentCard) {
			card.DefaultInputModes, card.DefaultOutputModes = []string{"application/json"}, []string{"image/png"}
			card.Skills = []sdk.AgentSkill{{ID: "read", InputModes: []string{"text/plain"}, OutputModes: []string{"text/plain"}}}
		}, valid: true},
		{name: "incompatible advertised skill", configure: func(card *sdk.AgentCard) {
			card.DefaultInputModes, card.DefaultOutputModes = []string{"text/plain"}, []string{"text/plain"}
			card.Skills = []sdk.AgentSkill{{ID: "read"}, {ID: "render", OutputModes: []string{"image/png"}}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var card sdk.AgentCard
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if err := jsonv2.MarshalWrite(w, &card); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			card = sdk.AgentCard{Name: "agent", SupportedInterfaces: []*sdk.AgentInterface{sdk.NewAgentInterface(server.URL+"/rpc", sdk.TransportProtocolJSONRPC)}}
			test.configure(&card)
			set, err := a2a.OpenToolSet(t.Context(), a2a.Endpoint{CardURL: server.URL, HTTPClient: server.Client()})
			if set != nil {
				defer func() { _ = set.Close() }()
			}
			if test.valid {
				if err != nil || set == nil {
					t.Fatalf("usable text capability was rejected: %v", err)
				}
			} else if set != nil || !errors.Is(err, a2a.ErrUnsupportedContent) || !errors.Is(err, a2a.ErrInvalidCard) {
				t.Fatalf("incompatible capability was admitted: %v, %v", set, err)
			}
		})
	}
}
