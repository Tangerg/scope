package httpclient

import (
	"cmp"
	"fmt"
	"net/http"
)

const DefaultMaxJSONResponseBytes = 32 << 20

const maximumRequestIDBytes = 128

// ResponseError is the one representation of a failed HTTP response across
// providers. It exposes transport evidence, including headers such as
// Retry-After, without turning a remote response body into public
// diagnostics. It makes no claim about provider execution. An SDK binding
// wraps its own error beside it, so the SDK error stays reachable through
// errors.As.
type ResponseError struct {
	statusCode int
	requestID  string
	header     http.Header
}

// Providers name their request identifier differently; these two headers
// cover the convention, and a value outside the safe grammar is dropped.
const (
	headerRequestID  = "Request-Id"
	headerXRequestID = "X-Request-Id"
)

func NewResponseError(statusCode int, header http.Header) *ResponseError {
	requestID := cmp.Or(header.Get(headerRequestID), header.Get(headerXRequestID))
	if !validRequestID(requestID) {
		requestID = ""
	}
	return &ResponseError{statusCode: statusCode, requestID: requestID, header: header.Clone()}
}

func (r *ResponseError) Error() string {
	message := fmt.Sprintf("http response: status %d", r.statusCode)
	if r.requestID != "" {
		message += " (request_id=" + r.requestID + ")"
	}
	return message
}

func (r *ResponseError) HTTPStatus() int { return r.statusCode }

func (r *ResponseError) HTTPHeader() http.Header { return r.header.Clone() }

func (r *ResponseError) RequestID() string { return r.requestID }

func validRequestID(value string) bool {
	if len(value) > maximumRequestIDBytes {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			continue
		}
		switch character {
		case '-', '_', '.', ':', '/':
		default:
			return false
		}
	}
	return true
}
