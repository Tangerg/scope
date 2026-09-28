package httpreq

import (
	"net/http"
	"strings"
)

type Method string

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
