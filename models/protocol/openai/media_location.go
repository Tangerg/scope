package openai

import (
	"fmt"

	"github.com/Tangerg/scope/core/media"
)

func mediaLocation(value *media.Media) (string, error) {
	switch value.Source.Kind {
	case media.SourceURI:
		return value.URI()
	case media.SourceBytes:
		return value.DataURI()
	default:
		return "", fmt.Errorf("source %q cannot be represented as a URL", value.Source.Kind)
	}
}
