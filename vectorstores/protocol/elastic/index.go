package elastic

import (
	"errors"
	"strings"

	"github.com/Tangerg/scope/core/httpclient"
)

const (
	mappingEndpoint  = "_mapping"
	settingsEndpoint = "_settings"
	searchEndpoint   = "_search"
)

type indexName string

func (i indexName) validate() error {
	name := string(i)
	if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, ",/*?\\") {
		return errors.New("elastic: Index must name one concrete index")
	}
	return nil
}

// path addresses endpoint on this index alone.
func (i indexName) path(endpoint string) string {
	return "/" + httpclient.PathSegment(string(i)) + "/" + endpoint
}
