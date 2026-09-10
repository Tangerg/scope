package a2a_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/Tangerg/scope/a2a"
)

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
				Agent: echoAgent{}, Card: &sdka2a.AgentCard{Name: "echo", SupportedInterfaces: interfaces},
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
	handler, err := a2a.NewHTTPHandler(a2a.ServerConfig{Agent: echoAgent{}, Card: card})
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
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || result.Error.Code != -32700 {
		t.Fatalf("advertised path status = %d, body = %s, want JSON-RPC parse error", response.Code, response.Body.String())
	}
}
