package httpreq

import (
	"net/http"
	"strings"
)

// Method is an HTTP method exposed by the tool contract.
type Method string

// Methods are an allowlist rather than a pass-through, because the tool
// decides what a model may do to a remote host. An unlisted method is refused
// before the request is built.
const (
	MethodGET    Method = http.MethodGet
	MethodHEAD   Method = http.MethodHead
	MethodPOST   Method = http.MethodPost
	MethodPUT    Method = http.MethodPut
	MethodPATCH  Method = http.MethodPatch
	MethodDELETE Method = http.MethodDelete
)

// Normalize applies the wire default and canonical HTTP casing, and rejects
// methods outside the tool contract.
func (m Method) Normalize() (Method, error) {
	normalized := Method(strings.ToUpper(strings.TrimSpace(string(m))))
	if normalized == "" {
		normalized = MethodGET
	}
	switch normalized {
	case MethodGET, MethodHEAD, MethodPOST, MethodPUT, MethodPATCH, MethodDELETE:
		return normalized, nil
	default:
		return "", ErrInvalidMethod
	}
}

func (m Method) Validate() error {
	_, err := m.Normalize()
	return err
}
