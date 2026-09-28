package storetest_test

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

type noopCloser struct{ validatingCapabilities }

func (noopCloser) Close() error { return nil }

func TestCapabilitiesOfDetectsACloser(t *testing.T) {
	t.Parallel()

	var _ vectorstore.Closer = noopCloser{}

	if got := storetest.CapabilitiesOf(noopCloser{}); !got.Closer {
		t.Fatalf("CapabilitiesOf(store with Close) = %+v, want Closer true", got)
	}
	if got := storetest.CapabilitiesOf(validatingCapabilities{}); got.Closer {
		t.Fatalf("CapabilitiesOf(store without Close) = %+v, want Closer false", got)
	}
}

func TestCapabilitiesOfReportsTheWholeSet(t *testing.T) {
	t.Parallel()

	want := storetest.Capabilities{
		Indexer: true, Searcher: true, IDDeleter: true, FilterDeleter: true,
	}
	if got := storetest.CapabilitiesOf(validatingCapabilities{}); got != want {
		t.Fatalf("CapabilitiesOf() = %+v, want %+v", got, want)
	}
	if got := storetest.CapabilitiesOf(struct{}{}); got != (storetest.Capabilities{}) {
		t.Fatalf("CapabilitiesOf(nothing) = %+v, want the zero set", got)
	}
}
