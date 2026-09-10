package a2a

import (
	"context"
	"fmt"
	"net/http"
	"time"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

// ToolConcurrencyPolicy decides whether calls to one endpoint may overlap.
// False keeps a call exclusive; true permits overlap except for equal non-empty
// keys. The invocation has passed schema validation. The callback must be
// deterministic, side-effect-free, and safe for concurrent use and replay.
type ToolConcurrencyPolicy func(toolcontract.Invocation) (key string, concurrent bool)

// Endpoint describes one remote A2A agent to expose as a chat tool. Its zero
// policy keeps discovery and RPC traffic on CardURL's origin.
type Endpoint struct {
	// Name overrides the model-visible tool name. Empty derives it from the
	// resolved AgentCard.
	Name string

	// CardURL is the absolute HTTP(S) URL used to resolve the AgentCard.
	CardURL string

	// HTTPClient is the client used for both card resolution and RPC calls.
	// Nil uses http.DefaultClient. The caller retains ownership; a restricted
	// shallow copy is used internally.
	HTTPClient *http.Client

	// CardTimeout bounds AgentCard resolution only. Zero selects 30 seconds; it
	// does not impose a timeout on long-running agent RPC calls.
	CardTimeout time.Duration

	// AllowedRPCOrigins adds trusted RPC origins beyond CardURL's own origin.
	// Entries use the exact "scheme://host[:port]" form. Empty prevents an
	// AgentCard from redirecting calls to another origin.
	AllowedRPCOrigins []string

	// ConcurrencyPolicy belongs to the host because distinct remote task IDs do
	// not establish resource independence. Nil keeps calls exclusive.
	ConcurrencyPolicy ToolConcurrencyPolicy
}

const defaultCardTimeout = 30 * time.Second

func dial(ctx context.Context, endpoint Endpoint) (*a2aclient.Client, *sdka2a.AgentCard, error) {
	if endpoint.CardURL == "" {
		return nil, nil, ErrEmptyCardURL
	}
	if endpoint.CardTimeout < 0 {
		return nil, nil, ErrInvalidCardTimeout
	}
	policy, err := newEndpointOriginPolicy(endpoint.CardURL, endpoint.AllowedRPCOrigins)
	if err != nil {
		return nil, nil, err
	}
	baseClient := endpoint.HTTPClient
	if baseClient == nil {
		baseClient = http.DefaultClient
	}
	cardClient := policy.cardOrigins.restrict(baseClient)

	cardTimeout := endpoint.CardTimeout
	if cardTimeout == 0 {
		cardTimeout = defaultCardTimeout
	}
	resolveCtx, cancelResolve := context.WithTimeout(ctx, cardTimeout)
	defer cancelResolve()
	resolver := agentcard.NewResolver(cardClient)
	card, err := resolver.Resolve(resolveCtx, endpoint.CardURL)
	if err != nil {
		return nil, nil, fmt.Errorf("a2a: resolve agent card at %q: %w", endpoint.CardURL, err)
	}
	if card == nil {
		return nil, nil, ErrNilCard
	}
	if validateCardErr := policy.validateCard(card); validateCardErr != nil {
		return nil, nil, validateCardErr
	}

	rpcClient := policy.rpcOrigins.restrict(baseClient)
	client, err := a2aclient.NewFromCard(ctx, card,
		a2aclient.WithJSONRPCTransport(rpcClient),
		a2aclient.WithRESTTransport(rpcClient),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("a2a: open client for agent %q: %w", card.Name, err)
	}
	return client, card, nil
}
