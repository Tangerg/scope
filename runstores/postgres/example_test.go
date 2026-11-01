package postgres_test

import (
	"fmt"
	"strings"

	"github.com/Tangerg/scope/runstores/postgres"
)

func ExampleSchemaSQL() {
	ddl, err := postgres.SchemaSQL("worker")
	if err != nil {
		panic(err)
	}
	// Provision once with a schema owner. Application connections do not need DDL privileges.
	fmt.Println(strings.Contains(ddl, `CREATE TABLE "worker".start_receipts`))
	// Output: true
}
