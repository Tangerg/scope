package pinecone

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

// Closer is true because this store creates a resource of its own: it closes
// the index connection it opened through Client.Index,
// not the caller's client. A store that was handed everything it holds
// implements nothing, so the flag also pins which of the two this is.
func TestStoreConformance(t *testing.T) {
	storetest.Run(t, new(Store), storetest.Capabilities{
		Indexer: true, Searcher: true, IDDeleter: true, FilterDeleter: true, Closer: true,
	})
}
