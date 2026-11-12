package a2a

import (
	"context"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/httpclient"
)

type ServerConfig struct {
	Agent Agent

	// Card is the AgentCard served at the well-known path. Required and
	// snapshotted during construction. SupportedInterfaces must contain exactly
	// one JSON-RPC interface using the SDK's protocol version and an absolute
	// HTTP(S) URL. Its path is the exact RPC route; hosts own public-origin
	// routing and reverse-proxy configuration. Its Capabilities must not declare
	// push notifications or an extended agent card, which this handler does not
	// provide. Default and skill input/output modes may declare only text/plain,
	// which the Agent boundary can represent. Streaming and input modes are
	// enforced from the published snapshot.
	Card *sdka2a.AgentCard

	// TaskStore persists A2A task state and is required: the Host owns the store
	// and its retention rather than inheriting a hidden default. The SDK's bundled
	// in-memory store (taskstore.NewInMemory) keeps every task for the process
	// lifetime with no eviction, so a long-running server must supply a store it
	// can bound; a short-lived one may pass that in-memory store explicitly.
	TaskStore taskstore.Store

	// MaxRequestBytes bounds one RPC request body. The protocol decoder buffers a
	// whole request before any authentication or admission can refuse it, so an
	// unbounded body lets any caller choose this server's memory use. Zero selects
	// 4 MiB, far above an ordinary text or JSON message; a Host exchanging larger
	// payloads raises it.
	MaxRequestBytes int64
}

// ProtocolServerConfig serves an already-bound A2A request handler.
type ProtocolServerConfig struct {
	// Card has the requirements of [ServerConfig.Card] apart from its text-only
	// modes; Handler owns the semantics of every capability it declares.
	Card *sdka2a.AgentCard

	Handler a2asrv.RequestHandler

	// MaxRequestBytes has the meaning of [ServerConfig.MaxRequestBytes].
	MaxRequestBytes int64
}

const defaultMaxRequestBytes int64 = 4 << 20

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
	return serveProtocol(config.Card, config.MaxRequestBytes, func(card *sdka2a.AgentCard) (a2asrv.RequestHandler, error) {
		enforcer, enforcerErr := a2asrv.NewAgentCardEnforcer(card)
		if enforcerErr != nil {
			return nil, fmt.Errorf("%w %q: %w", ErrInvalidCard, card.Name, enforcerErr)
		}
		if err := (textProjection{}).validateCard(card); err != nil {
			return nil, err
		}
		return a2asrv.NewHandler(exec,
			a2asrv.WithCallInterceptors(enforcer),
			a2asrv.WithTaskStore(config.TaskStore),
			// The SDK's default worker recovery formats private panic values into logs.
			a2asrv.WithExecutionPanicHandler(func(any) error { return sdka2a.ErrInternalError }),
		), nil
	})
}

// NewProtocolHTTPHandler serves a fixed agent card and an already-bound A2A
// request handler on its declared JSON-RPC route.
func NewProtocolHTTPHandler(config ProtocolServerConfig) (http.Handler, error) {
	if lo.IsNil(config.Handler) {
		return nil, ErrNilProtocolHandler
	}
	return serveProtocol(config.Card, config.MaxRequestBytes, func(*sdka2a.AgentCard) (a2asrv.RequestHandler, error) {
		return config.Handler, nil
	})
}

func serveProtocol(source *sdka2a.AgentCard, maxRequestBytes int64, bind func(*sdka2a.AgentCard) (a2asrv.RequestHandler, error)) (http.Handler, error) {
	if source == nil {
		return nil, ErrNilCard
	}
	switch {
	case maxRequestBytes < 0:
		return nil, ErrInvalidMaxRequestBytes
	case maxRequestBytes == 0:
		maxRequestBytes = defaultMaxRequestBytes
	}
	card, encoded, err := freezeCard(source)
	if err != nil {
		return nil, err
	}
	if err = validateServedCapabilities(card); err != nil {
		return nil, err
	}
	rpcPath, err := serverRPCPath(card)
	if err != nil {
		return nil, err
	}
	requestHandler, err := bind(card)
	if err != nil {
		return nil, err
	}
	cardHandler := a2asrv.NewAgentCardHandler(staticAgentCard{card: card, encoded: encoded})

	rpcHandler := http.MaxBytesHandler(a2asrv.NewJSONRPCHandler(&a2asrv.InterceptedHandler{
		Handler: requestHandler, Interceptors: []a2asrv.CallInterceptor{protocolErrors{}},
	}), maxRequestBytes)
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

// The fixed handler installs no push store, sender, or extended-card producer.
func validateServedCapabilities(card *sdka2a.AgentCard) error {
	switch {
	case card.Capabilities.PushNotifications:
		return fmt.Errorf("%w %q: push notifications are not supported", ErrInvalidCard, card.Name)
	case card.Capabilities.ExtendedAgentCard:
		return fmt.Errorf("%w %q: an extended agent card is not supported", ErrInvalidCard, card.Name)
	default:
		return nil
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
	endpoint, err := httpclient.ParseBaseURL(iface.URL)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidRPCInterface, err)
	}
	if endpoint.Path == "" || endpoint.Path == a2asrv.WellKnownAgentCardPath {
		return "", fmt.Errorf("%w: URL requires a distinct RPC path", ErrInvalidRPCInterface)
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
