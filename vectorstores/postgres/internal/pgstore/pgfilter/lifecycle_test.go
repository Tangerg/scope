package pgfilter_test

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/storetest"
	"github.com/Tangerg/scope/vectorstores/postgres/internal/pgstore/pgfilter"
)

func TestVisitorLifecycle(t *testing.T) {
	t.Parallel()
	storetest.VisitorLifecycle(t, func() storetest.Compiler {
		compiler := pgfilter.NewCompiler("metadata")
		return storetest.Compiler{Visit: compiler.Visit, Snapshot: func() any { return compiler.Result() }}
	})
}
