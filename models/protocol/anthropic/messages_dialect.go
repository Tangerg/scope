package anthropic

import (
	"errors"
	"fmt"
	"slices"

	anthropicsdk "github.com/anthropics/anthropic-sdk-go/v2"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/metadata"
)

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

// reasoningEfforts is the output_config.effort vocabulary. Core's effort is
// the wire word itself, so admitting it is the whole mapping.
var reasoningEfforts = []corechat.ReasoningEffort{
	corechat.ReasoningEffort(anthropicsdk.OutputConfigEffortLow),
	corechat.ReasoningEffort(anthropicsdk.OutputConfigEffortMedium),
	corechat.ReasoningEffort(anthropicsdk.OutputConfigEffortHigh),
	corechat.ReasoningEffort(anthropicsdk.OutputConfigEffortXhigh),
	corechat.ReasoningEffort(anthropicsdk.OutputConfigEffortMax),
}

func (d Dialect) validateOptions(options corechat.Options) error {
	if options.FrequencyPenalty != nil {
		return errors.New("anthropic: options.frequency_penalty is not supported")
	}
	if options.PresencePenalty != nil {
		return errors.New("anthropic: options.presence_penalty is not supported")
	}
	// Revision and sampling support are independent endpoint capabilities;
	// metadata headers cannot enable request fields.
	for _, sampling := range []struct {
		name string
		set  bool
	}{
		{requestFieldTemperature, options.Temperature != nil},
		{requestFieldTopK, options.TopK != nil},
		{requestFieldTopP, options.TopP != nil},
	} {
		if sampling.set && !d.SupportsSampling {
			return fmt.Errorf("anthropic: options.%s is not supported by %s Messages", sampling.name, d.Provider)
		}
	}
	if options.ReasoningEffort != "" && !slices.Contains(reasoningEfforts, options.ReasoningEffort) {
		return fmt.Errorf("anthropic: options.reasoning_effort has unsupported value %q", options.ReasoningEffort)
	}
	if err := mapProtocolOutputFormat(options.OutputFormat, d, &anthropicsdk.MessageNewParams{}); err != nil {
		return err
	}
	fields, err := d.nativeFields(options.Extensions)
	if err != nil {
		return err
	}
	_, err = decodeOutputConfig(fields, protocolRequestExtensionKey(d.Provider))
	return err
}

// nativeFields decodes the provider's native request fields; none of them may
// be a field the Core projection owns.
func (d Dialect) nativeFields(extensions metadata.Extensions) (map[string]any, error) {
	extensionKey := protocolRequestExtensionKey(d.Provider)
	encoded, _, err := extensions.Decode[metadata.Map](extensionKey)
	if err != nil {
		return nil, fmt.Errorf("anthropic: extension %q: %w", extensionKey, err)
	}
	fields, err := encoded.Values()
	if err != nil {
		return nil, err
	}
	return fields, rejectCoreOwnedFields(fields, extensionKey)
}

func protocolRequestExtensionKey(provider string) string {
	if provider == Provider {
		return RequestExtensionKey
	}
	return provider + CompatibleRequestExtensionSuffix
}

func protocolStreamEventExtensionKey(provider string) string {
	if provider == Provider {
		return StreamEventExtensionKey
	}
	return provider + CompatibleStreamExtensionSuffix
}
