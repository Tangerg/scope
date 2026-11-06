package protocol

import (
	"net/http"
)

type responseError struct {
	err    error
	status int
	header http.Header
}

func newResponseError(err error, status int, header http.Header) *responseError {
	return &responseError{err: err, status: status, header: header.Clone()}
}

func (r *responseError) Error() string           { return r.err.Error() }
func (r *responseError) Unwrap() error           { return r.err }
func (r *responseError) HTTPStatus() int         { return r.status }
func (r *responseError) HTTPHeader() http.Header { return r.header.Clone() }
