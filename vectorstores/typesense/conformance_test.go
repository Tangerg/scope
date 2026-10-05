package typesense

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func TestStoreConformance(t *testing.T) {
	storetest.Run(t, new(Store), storetest.Capabilities{
		Indexer: true, Searcher: true, HybridSearch: true, IDDeleter: true, FilterDeleter: false,
	})
}
