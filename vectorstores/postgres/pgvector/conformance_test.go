package pgvector

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/storetest"
	"github.com/Tangerg/scope/vectorstores/postgres"
)

func TestStoreConformance(t *testing.T) {
	storetest.Run(t, new(postgres.Store), storetest.Capabilities{
		Upserter: true, Querier: true, IDDeleter: true,
	})
}
