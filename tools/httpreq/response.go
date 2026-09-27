package httpreq

import (
	"io"

	"github.com/Tangerg/scope/tools/content"
)

// Response is the model-facing result. Body and header values preserve bytes;
// UTF-8 remains readable text and other bytes use explicit base64 on the wire.
// Alongside an error from Client.Do it contains incomplete observations only;
// Truncated reports the configured size cap, not successful body completion.
type Response struct {
	Status    int                          `json:"status"`
	Headers   map[string][]content.Content `json:"headers,omitempty"`
	Body      content.Content              `json:"body"`
	Truncated bool                         `json:"truncated,omitzero"`
	// Duration includes transport execution, body reading, and body closure.
	Duration string `json:"duration"`
}

func readCapped(reader io.Reader, maxBytes int64) ([]byte, bool, error) {
	limited := io.LimitReader(reader, maxBytes+1)
	body, err := io.ReadAll(limited)
	if int64(len(body)) > maxBytes {
		return body[:maxBytes], true, err
	}
	return body, false, err
}
