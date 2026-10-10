package a2a_test

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/Tangerg/scope/a2a"
)

func TestServerRejectsCapabilitiesItCannotFulfill(t *testing.T) {
	iface := []*sdka2a.AgentInterface{sdka2a.NewAgentInterface("https://agent.example/rpc", sdka2a.TransportProtocolJSONRPC)}
	for name, capabilities := range map[string]sdka2a.AgentCapabilities{
		"push notifications": {PushNotifications: true},
		"extended card":      {ExtendedAgentCard: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := a2a.NewHTTPHandler(a2a.ServerConfig{
				Agent:     echoAgent{},
				Card:      &sdka2a.AgentCard{Name: "echo", SupportedInterfaces: iface, Capabilities: capabilities},
				TaskStore: testTaskStore(t),
			})
			if !errors.Is(err, a2a.ErrInvalidCard) {
				t.Fatalf("NewHTTPHandler error = %v, want ErrInvalidCard", err)
			}
		})
	}
	// The executor streams, so a card may advertise streaming.
	if _, err := a2a.NewHTTPHandler(a2a.ServerConfig{
		Agent:     echoAgent{},
		Card:      &sdka2a.AgentCard{Name: "echo", SupportedInterfaces: iface, Capabilities: sdka2a.AgentCapabilities{Streaming: true}},
		TaskStore: testTaskStore(t),
	}); err != nil {
		t.Fatalf("streaming card rejected: %v", err)
	}
}

func TestServerServesCardAsItsV2Encoding(t *testing.T) {
	card := &sdka2a.AgentCard{
		Name:                "echo",
		SupportedInterfaces: []*sdka2a.AgentInterface{sdka2a.NewAgentInterface("https://agent.example/rpc", sdka2a.TransportProtocolJSONRPC)},
		Capabilities:        sdka2a.AgentCapabilities{Streaming: true},
	}
	handler, err := a2a.NewHTTPHandler(a2a.ServerConfig{Agent: echoAgent{}, Card: card, TaskStore: testTaskStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	// The served bytes must be the one v2 encoding frozen at construction, never
	// a second encoding through the SDK's JSON v1 path.
	want, err := jsonv2.Marshal(card)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, a2asrv.WellKnownAgentCardPath, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("card status = %d", recorder.Code)
	}
	if !bytes.Equal(recorder.Body.Bytes(), want) {
		t.Fatalf("served card = %s, want its JSON v2 encoding %s", recorder.Body.Bytes(), want)
	}
}

func TestServerRejectsInterfacesItCannotServe(t *testing.T) {
	jsonRPC := func(endpoint string) *sdka2a.AgentInterface {
		return sdka2a.NewAgentInterface(endpoint, sdka2a.TransportProtocolJSONRPC)
	}
	for name, interfaces := range map[string][]*sdka2a.AgentInterface{
		"missing":             nil,
		"nil":                 {nil},
		"multiple":            {jsonRPC("https://agent.example/one"), jsonRPC("https://agent.example/two")},
		"REST":                {sdka2a.NewAgentInterface("https://agent.example/invoke", sdka2a.TransportProtocolHTTPJSON)},
		"unsupported version": {{URL: "https://agent.example/invoke", ProtocolBinding: sdka2a.TransportProtocolJSONRPC, ProtocolVersion: "0.0"}},
		"relative URL":        {jsonRPC("/invoke")},
		"unsupported scheme":  {jsonRPC("ftp://agent.example/invoke")},
		"malformed URL":       {jsonRPC("https://agent.example/%zz")},
		"missing path":        {jsonRPC("https://agent.example")},
		"credentials":         {jsonRPC("https://user@agent.example/invoke")},
		"query":               {jsonRPC("https://agent.example/invoke?tenant=one")},
		"empty query":         {jsonRPC("https://agent.example/invoke?")},
		"fragment":            {jsonRPC("https://agent.example/invoke#one")},
		"card conflict":       {jsonRPC("https://agent.example" + a2asrv.WellKnownAgentCardPath)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := a2a.NewHTTPHandler(a2a.ServerConfig{
				Agent: echoAgent{}, Card: &sdka2a.AgentCard{Name: "echo", SupportedInterfaces: interfaces}, TaskStore: testTaskStore(t),
			})
			if !errors.Is(err, a2a.ErrInvalidRPCInterface) {
				t.Fatalf("NewHTTPHandler error = %v", err)
			}
		})
	}
}

func TestServerRoutesOnlyTheAdvertisedPath(t *testing.T) {
	card := &sdka2a.AgentCard{
		Name: "echo",
		SupportedInterfaces: []*sdka2a.AgentInterface{
			sdka2a.NewAgentInterface("https://agent.example/rpc/{literal}/", sdka2a.TransportProtocolJSONRPC),
		},
	}
	handler, err := a2a.NewHTTPHandler(a2a.ServerConfig{Agent: echoAgent{}, Card: card, TaskStore: testTaskStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	card.SupportedInterfaces[0].URL = "https://agent.example/changed"
	for _, path := range []string{"/invoke", "/changed", "/rpc/other/", "/rpc/{literal}/child"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("unadvertised path %q status = %d", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/rpc/{literal}/", nil))
	var result struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := jsonv2.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || result.Error.Code != -32700 {
		t.Fatalf("advertised path status = %d, body = %s, want JSON-RPC parse error", response.Code, response.Body.String())
	}
}
