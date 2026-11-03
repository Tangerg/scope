package storetest_test

import (
	"testing"

	"github.com/Tangerg/scope/core/history/inmemory"
	"github.com/Tangerg/scope/core/history/storetest"
)

func TestRun(t *testing.T) {
	storetest.Run(t, new(inmemory.Store), storetest.Capabilities{
		Reader: true, Writer: true, Clearer: true,
		Lister: true,
	})
}

func TestRunAtomic(t *testing.T) {
	storetest.RunAtomic(t, new(inmemory.Store))
}
