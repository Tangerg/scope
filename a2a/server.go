package a2a

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// ServerConfig wires a [Agent] into an HTTP A2A endpoint.
type ServerConfig struct {
	// Agent is the capability served over A2A. Required.
	Agent Agent

	// Card is the AgentCard served at the well-known path. Required and
	// snapshotted during construction. SupportedInterfaces must contain exactly
	// one JSON-RPC interface using the SDK's protocol version and an absolute
	// HTTP(S) URL. Its path is the exact RPC route; hosts own public-origin
	// routing and reverse-proxy configuration.
	Card *sdka2a.AgentCard
}

// NewHTTPHandler returns a plain [http.Handler] rather than starting a server,
// so the host keeps ownership of the listener, TLS, timeouts, and middleware.
// The card is encoded during construction because an AgentCard that cannot be
// marshaled would otherwise fail at the well-known path, where a peer reads it
// as an unreachable agent rather than a misconfigured one.
func NewHTTPHandler(config ServerConfig) (http.Handler, error) {
	exec, err := newExecutor(config.Agent)
	if err != nil {
		return nil, err
	}
	if config.Card == nil {
		return nil, ErrNilCard
	}
	cardHandler, err := newStaticAgentCardHandler(config.Card)
	if err != nil {
		return nil, err
	}
	rpcPath, err := serverRPCPath(config.Card)
	if err != nil {
		return nil, err
	}

	requestHandler := a2asrv.NewHandler(exec)

	rpcHandler := a2asrv.NewJSONRPCHandler(requestHandler)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case a2asrv.WellKnownAgentCardPath:
			cardHandler.ServeHTTP(w, r)
		case rpcPath:
			rpcHandler.ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}), nil
}

func newStaticAgentCardHandler(card *sdka2a.AgentCard) (http.Handler, error) {
	if _, err := json.Marshal(card); err != nil {
		return nil, fmt.Errorf("%w %q: encode: %w", ErrInvalidCard, card.Name, err)
	}
	return a2asrv.NewStaticAgentCardHandler(card), nil
}

func serverRPCPath(card *sdka2a.AgentCard) (string, error) {
	if len(card.SupportedInterfaces) != 1 || card.SupportedInterfaces[0] == nil ||
		card.SupportedInterfaces[0].ProtocolBinding != sdka2a.TransportProtocolJSONRPC ||
		card.SupportedInterfaces[0].ProtocolVersion != sdka2a.Version {
		return "", fmt.Errorf("%w: exactly one JSON-RPC interface using protocol %s is required", ErrInvalidRPCInterface, sdka2a.Version)
	}
	endpoint, err := url.Parse(card.SupportedInterfaces[0].URL)
	if err != nil {
		return "", fmt.Errorf("%w: parse URL: %w", ErrInvalidRPCInterface, err)
	}
	if _, err := originFromURL(endpoint); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidRPCInterface, err)
	}
	if endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" ||
		endpoint.Path == "" || endpoint.Path == a2asrv.WellKnownAgentCardPath {
		return "", fmt.Errorf("%w: URL requires a distinct RPC path without user info, query, or fragment", ErrInvalidRPCInterface)
	}
	return endpoint.Path, nil
}
