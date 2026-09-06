package httpreq

import (
	"io"
)

// Response is the model-facing result. Body remains text so binary or
// structured payload interpretation stays with the caller.
type Response struct {
	Status    int                 `json:"status"`
	Headers   map[string][]string `json:"headers,omitempty"`
	Body      string              `json:"body"`
	Truncated bool                `json:"truncated,omitempty"`
	// Duration includes transport execution, body reading, and body closure.
	Duration string `json:"duration"`
}

func readCapped(reader io.Reader, maxBytes int64) ([]byte, bool, error) {
	limited := io.LimitReader(reader, maxBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if int64(len(body)) > maxBytes {
		return body[:maxBytes], true, nil
	}
	return body, false, nil
}
