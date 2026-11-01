package neo4j

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func TestStoreConformance(t *testing.T) {
	storetest.Run(t, new(Store), storetest.Capabilities{
		Upserter: true, IDDeleter: true,
	})
}
