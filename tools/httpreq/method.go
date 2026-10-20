package httpreq

import (
	"net/http"
	"slices"
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

var methodVocabulary = [...]Method{MethodGET, MethodHEAD, MethodPOST, MethodPUT, MethodPATCH, MethodDELETE}

func (Method) JSONSchemaEnum() []string {
	values := make([]string, len(methodVocabulary))
	for index, value := range methodVocabulary {
		values[index] = string(value)
	}
	return values
}

// Normalize applies the wire default and canonical HTTP casing, and rejects
// methods outside the tool contract.
func (m Method) Normalize() (Method, error) {
	normalized := Method(strings.ToUpper(strings.TrimSpace(string(m))))
	if normalized == "" {
		normalized = MethodGET
	}
	if slices.Contains(methodVocabulary[:], normalized) {
		return normalized, nil
	}
	return "", ErrInvalidMethod
}

func (m Method) Validate() error {
	_, err := m.Normalize()
	return err
}

// carriesBody reports whether a request body is sent for m. The transport
// drops a GET or HEAD body, so admission must refuse one rather than send a
// request other than the one asked for.
func (m Method) carriesBody() bool {
	return m != MethodGET && m != MethodHEAD
}
