package elastic

import (
	"errors"
	"strings"
)

type indexName string

func (i indexName) validate() error {
	name := string(i)
	if name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, ",/*?\\") {
		return errors.New("elastic: Index must name one concrete index")
	}
	return nil
}
