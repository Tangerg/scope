package anthropic

import corechat "github.com/Tangerg/scope/core/chat"

const (
	CompatibleRequestExtensionSuffix = "/anthropic_request"
	CompatibleStreamExtensionSuffix  = "/anthropic_stream_event"
)

// Dialect declares the protocol differences selected by an
// Anthropic-compatible provider adapter.
type Dialect struct {
	// Provider scopes opaque reasoning state to the API that issued it. It is
	// required even when the provider uses Anthropic's wire shape because signed
	// thinking is not portable across vendors.
	Provider         string
	Revision         MessagesRevision
	APIKeyScheme     APIKeyScheme
	SupportsSampling bool
	// NativeJSONSchema reports whether this endpoint implements Anthropic's
	// output_config.format JSON Schema control. Compatible providers that do not
	// declare it reject that output format with ErrUnsupportedOutputFormat.
	NativeJSONSchema bool
	// PrepareRequest applies provider policy to the resolved, independently
	// owned request before the Messages wire projection is built. Fields holds
	// the decoded native request extension and may be changed by the provider.
	PrepareRequest func(*corechat.Request, map[string]any) error
}

func protocolRequestExtensionKey(provider string) string {
	if provider == ProviderID {
		return RequestExtensionKey
	}
	return provider + CompatibleRequestExtensionSuffix
}

func protocolStreamEventExtensionKey(provider string) string {
	if provider == ProviderID {
		return StreamEventExtensionKey
	}
	return provider + CompatibleStreamExtensionSuffix
}
