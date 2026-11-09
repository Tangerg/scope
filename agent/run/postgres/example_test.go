package postgres_test

import (
	"fmt"
	"strings"

	"github.com/Tangerg/scope/agent/run/postgres"
)

func ExampleSchemaSQL() {
	schema, err := postgres.SchemaSQL("agent_execution")
	if err != nil {
		panic(err)
	}
	fmt.Println(strings.Contains(schema, `CREATE TABLE "agent_execution".log`))
	// Output:
	// true
}
