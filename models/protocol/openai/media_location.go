package openai

import (
	"encoding/base64"
	"fmt"

	"github.com/Tangerg/scope/core/media"
)

const (
	dataURLPrefix          = "data:"
	base64DataURLSeparator = ";base64,"
)

func mediaLocation(value *media.Media) (string, error) {
	switch value.Source.Kind {
	case media.SourceURI:
		return value.URI()
	case media.SourceBytes:
		data, err := value.Bytes()
		if err != nil {
			return "", err
		}
		return dataURLPrefix + value.MIME + base64DataURLSeparator + base64.StdEncoding.EncodeToString(data), nil
	default:
		return "", fmt.Errorf("source %q cannot be represented as a URL", value.Source.Kind)
	}
}
