package storetest_test

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

// noopCloser is the shape this capability exists to separate out: a store that
// was handed everything it holds, answering the lifecycle question with a Close
// that releases nothing. Twenty-three adapters answered that way — one of them
// by closing the client its caller had injected — and a caller could not tell
// any of them from a store that really did need closing.
type noopCloser struct{ validatingCapabilities }

func (noopCloser) Close() error { return nil }

// A Close method is what makes a store a Closer, so a store that owns nothing
// and declares no capability must not have one. [storetest.Run] compares the
// two sets, and this pins the detection that comparison rests on.
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

// The rest of the set keeps working the same way, so a store cannot gain or
// lose a capability without the declared set moving with it.
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
