package protocol

import "github.com/Tangerg/scope/core/media"

func validateProtocolMedia(provider string, value *media.Media) error {
	// Gemini's ordinary FileData can resolve a URI's MIME type remotely; Vertex
	// and inline bytes require an explicit type. A filename never supplies it.
	if provider == GoogleProviderID && value != nil && value.Source.Kind == media.SourceURI {
		return value.Validate()
	}
	return value.RequireMIME()
}
