package a2a

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/url"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"

	"github.com/samber/lo"
)

type ServerConfig struct {
	Agent Agent

	// Card is the AgentCard served at the well-known path. Required and
	// snapshotted during construction. SupportedInterfaces must contain exactly
	// one JSON-RPC interface using the SDK's protocol version and an absolute
	// HTTP(S) URL. Its path is the exact RPC route; hosts own public-origin
	// routing and reverse-proxy configuration. Its Capabilities must not declare
	// push notifications or an extended agent card, which this handler does not
	// provide; the declared streaming flag is enforced as advertised.
	Card *sdka2a.AgentCard

	// TaskStore persists A2A task state and is required: the Host owns the store
	// and its retention rather than inheriting a hidden default. The SDK's bundled
	// in-memory store (taskstore.NewInMemory) keeps every task for the process
	// lifetime with no eviction, so a long-running server must supply a store it
	// can bound; a short-lived one may pass that in-memory store explicitly.
	TaskStore taskstore.Store
}

// NewHTTPHandler leaves listener, TLS, timeouts, and middleware ownership with
// the Host. The assembled request handler owns which protocol capabilities this
// server actually provides; the card projects that one fact. Construction
// rejects unencodable cards and any capability the fixed handler cannot fulfill.
func NewHTTPHandler(config ServerConfig) (http.Handler, error) {
	exec, err := newExecutor(config.Agent)
	if err != nil {
		return nil, err
	}
	if config.Card == nil {
		return nil, ErrNilCard
	}
	if lo.IsNil(config.TaskStore) {
		return nil, ErrNilTaskStore
	}

	// Freeze the card into the bytes this handler serves and decode a copy it
	// owns. Those frozen bytes are the single representation of what the server
	// advertises and admits: the gate, the RPC route, and the served card all
	// derive from them, so later mutation of config.Card or its Extensions slice
	// changes none of them.
	card, encoded, err := freezeCard(config.Card)
	if err != nil {
		return nil, err
	}
	capabilities, err := servedCapabilities(card)
	if err != nil {
		return nil, err
	}
	rpcPath, err := serverRPCPath(card)
	if err != nil {
		return nil, err
	}

	requestHandler := a2asrv.NewHandler(exec,
		a2asrv.WithCapabilityChecks(&capabilities),
		a2asrv.WithTaskStore(config.TaskStore),
	)
	cardHandler := a2asrv.NewAgentCardHandler(staticAgentCard{card: card, encoded: encoded})

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

// servedCapabilities is the single capability set the handler advertises and
// enforces. The executor streams, so streaming follows the card's declaration;
// the fixed construction wires no push store, sender, or extended-card producer,
// so a card may not advertise either without the handler misrepresenting itself.
func servedCapabilities(card *sdka2a.AgentCard) (sdka2a.AgentCapabilities, error) {
	capabilities := card.Capabilities
	switch {
	case capabilities.PushNotifications:
		return sdka2a.AgentCapabilities{}, fmt.Errorf("%w %q: push notifications are not supported", ErrInvalidCard, card.Name)
	case capabilities.ExtendedAgentCard:
		return sdka2a.AgentCapabilities{}, fmt.Errorf("%w %q: an extended agent card is not supported", ErrInvalidCard, card.Name)
	default:
		return capabilities, nil
	}
}

// freezeCard encodes the caller's card into the one byte representation this
// handler serves, then decodes a copy the handler owns. The round-trip through
// the validated v2 encoding is the whole freeze: the caller keeps no reference to
// the owned card or its slices, so no element of Capabilities.Extensions can be
// rewritten after construction to change admission. Encoding here also avoids the
// SDK's JSON v1 re-encode, whose rules differ from the v2 form and which panics
// on failure.
func freezeCard(card *sdka2a.AgentCard) (*sdka2a.AgentCard, []byte, error) {
	encoded, err := jsonv2.Marshal(card)
	if err != nil {
		return nil, nil, fmt.Errorf("%w %q: encode: %w", ErrInvalidCard, card.Name, err)
	}
	owned := new(sdka2a.AgentCard)
	if err := jsonv2.Unmarshal(encoded, owned); err != nil {
		return nil, nil, fmt.Errorf("%w %q: decode: %w", ErrInvalidCard, card.Name, err)
	}
	return owned, encoded, nil
}

// staticAgentCard serves the card from the bytes frozen at construction. Both the
// bytes and the card it returns are owned by the handler and derive from the same
// freeze, so serving never re-encodes and the caller cannot mutate either.
type staticAgentCard struct {
	card    *sdka2a.AgentCard
	encoded []byte
}

func (s staticAgentCard) Card(context.Context) (*sdka2a.AgentCard, error) { return s.card, nil }

func (s staticAgentCard) CardJSON(context.Context) ([]byte, error) { return s.encoded, nil }

func serverRPCPath(card *sdka2a.AgentCard) (string, error) {
	iface, err := servedRPCInterface(card)
	if err != nil {
		return "", err
	}
	endpoint, err := url.Parse(iface.URL)
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

func servedRPCInterface(card *sdka2a.AgentCard) (*sdka2a.AgentInterface, error) {
	errServed := fmt.Errorf("%w: exactly one JSON-RPC interface using protocol %s is required", ErrInvalidRPCInterface, sdka2a.Version)
	if len(card.SupportedInterfaces) != 1 {
		return nil, errServed
	}
	iface := card.SupportedInterfaces[0]
	if iface == nil || iface.ProtocolBinding != sdka2a.TransportProtocolJSONRPC || iface.ProtocolVersion != sdka2a.Version {
		return nil, errServed
	}
	return iface, nil
}
