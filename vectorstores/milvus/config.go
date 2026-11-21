package milvus

import (
	"errors"
	"regexp"

	"github.com/milvus-io/milvus/client/v3/milvusclient"

	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	// Provider is the stable backend name for host-side attribution.
	Provider              = "milvus"
	fieldID               = "id"
	fieldVector           = "vector"
	fieldContent          = "content"
	fieldMeta             = "metadata"
	nativeMaxVarCharBytes = 65535
	sourcePageSize        = 16
	identityGroupSize     = 128
	nativeMaxTopK         = 16384
)

// documentFields are the columns of one stored document; every read requests
// exactly these and admits no others.
var documentFields = []string{fieldID, fieldContent, fieldMeta, fieldVector}

var collectionNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,254}$`)

type StoreConfig struct {
	// Client owns authentication, transport, retries and lifetime.
	Client         *milvusclient.Client
	CollectionName string
	Space          vectorstore.SpaceID
}

func (s StoreConfig) Validate() error {
	if s.Client == nil {
		return ErrMissingClient
	}
	if s.CollectionName == "" {
		return ErrMissingCollectionName
	}
	if !collectionNamePattern.MatchString(s.CollectionName) {
		return errors.New("milvus: invalid CollectionName")
	}
	return s.Space.Validate()
}
