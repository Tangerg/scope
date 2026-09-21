package anthropic

// Dialect declares the protocol differences selected by an
// Anthropic-compatible provider adapter.
type Dialect struct {
	// Provider scopes opaque reasoning state to the API that issued it. It is
	// required even when the provider uses Anthropic's wire shape because signed
	// thinking is not portable across vendors.
	Provider       string
	MaxTemperature float64
	RejectTopK     bool
	RejectTopP     bool
	// NativeJSONSchema reports whether this endpoint implements Anthropic's
	// output_config.format JSON Schema control. Compatible providers that do not
	// declare it receive the shared prompt fallback.
	NativeJSONSchema bool
}

func protocolRequestExtensionKey(provider string) string {
	if provider == protocolProvider {
		return RequestExtensionKey
	}
	return provider + "/anthropic_request"
}

func protocolStreamEventExtensionKey(provider string) string {
	if provider == protocolProvider {
		return StreamEventExtensionKey
	}
	return provider + "/anthropic_stream_event"
}
