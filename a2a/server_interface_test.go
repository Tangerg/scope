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

// extensionRequiredCode is the JSON-RPC code the SDK maps ErrExtensionSupportRequired to.
const extensionRequiredCode = -32008

// getTaskExtensionCode constructs a handler for a card carrying one extension,
// optionally flips that existing element to required after construction, sends a
// GetTask whose client activates no extension, and returns the JSON-RPC error code.
func getTaskExtensionCode(t *testing.T, required, flipAfterBuild bool) int {
	t.Helper()
	card := &sdka2a.AgentCard{
		Name:                "echo",
		SupportedInterfaces: []*sdka2a.AgentInterface{sdka2a.NewAgentInterface("https://agent.example/rpc", sdka2a.TransportProtocolJSONRPC)},
		Capabilities:        sdka2a.AgentCapabilities{Extensions: []sdka2a.AgentExtension{{URI: "urn:example:extension", Required: required}}},
	}
	handler, err := a2a.NewHTTPHandler(a2a.ServerConfig{Agent: echoAgent{}, Card: card, TaskStore: testTaskStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	if flipAfterBuild {
		card.Capabilities.Extensions[0].Required = true
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"missing"}}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/rpc", bytes.NewReader([]byte(body))))
	var result struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := jsonv2.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v, body=%s", err, response.Body.String())
	}
	if result.Error == nil {
		return 0
	}
	return result.Error.Code
}

// The admission fact belongs to the construction boundary. A required extension
// declared at construction must gate requests, but flipping an existing extension
// element to required afterwards must not, because the caller no longer owns the
// frozen capability the handler admits by.
func TestServerFreezesExtensionAdmissionAgainstCallerMutation(t *testing.T) {
	// Control: the gate is reachable and rejects a missing required extension.
	if code := getTaskExtensionCode(t, true, false); code != extensionRequiredCode {
		t.Fatalf("required extension declared at construction: error code = %d, want %d", code, extensionRequiredCode)
	}
	// Mutating the existing element after construction must not change admission.
	if code := getTaskExtensionCode(t, false, true); code == extensionRequiredCode {
		t.Fatal("flipping an existing extension element to required after construction changed admission")
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
