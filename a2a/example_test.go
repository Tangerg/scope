package a2a_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/Tangerg/scope/a2a"
)

func ExampleNewHTTPHandler() {
	handler, err := a2a.NewHTTPHandler(a2a.ServerConfig{
		Agent: echoAgent{},
		Card: &sdka2a.AgentCard{
			Name: "Echo",
			SupportedInterfaces: []*sdka2a.AgentInterface{
				sdka2a.NewAgentInterface("https://agent.example/custom/rpc", sdka2a.TransportProtocolJSONRPC),
			},
		},
	})
	if err != nil {
		panic(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, a2asrv.WellKnownAgentCardPath, nil))
	var card sdka2a.AgentCard
	if err := json.Unmarshal(response.Body.Bytes(), &card); err != nil {
		panic(err)
	}
	fmt.Println(card.SupportedInterfaces[0].URL)
	// Output: https://agent.example/custom/rpc
}
