package cassandra

import (
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/storetest"
)

func TestVisitorLifecycle(t *testing.T) {
	t.Parallel()
	storetest.VisitorLifecycle(t, func() storetest.Compiler {
		visitor := newVisitor([]MetadataColumn{{Name: "a", CQLType: "int"}, {Name: "b", CQLType: "int"}})
		return storetest.Compiler{Visit: visitor.Visit, Snapshot: func() any {
			query, args := visitor.snapshot()
			return struct {
				query string
				args  any
			}{query: query, args: args}
		}}
	})
}
