package httpclient

import "fmt"

const DefaultMaxJSONResponseBytes = 32 << 20

const maximumRequestIDBytes = 128

// ResponseError exposes transport evidence without turning a remote response
// body into public diagnostics. It makes no claim about provider execution.
type ResponseError struct {
	statusCode int
	requestID  string
}

func NewResponseError(statusCode int, requestID string) *ResponseError {
	if !validRequestID(requestID) {
		requestID = ""
	}
	return &ResponseError{statusCode: statusCode, requestID: requestID}
}

func (r *ResponseError) Error() string {
	message := fmt.Sprintf("http response: status %d", r.statusCode)
	if r.requestID != "" {
		message += " (request_id=" + r.requestID + ")"
	}
	return message
}

func (r *ResponseError) HTTPStatus() int { return r.statusCode }

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
