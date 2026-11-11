package elasticsearch

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/storetest"
	"github.com/Tangerg/scope/vectorstores/protocol/elastic"
)

func TestStoreConformance(t *testing.T) {
	storetest.Run(t, new(elastic.Store), storetest.Capabilities{
		Upserter: true, Querier: true, IDDeleter: true, FilterDeleter: false,
	})
}
