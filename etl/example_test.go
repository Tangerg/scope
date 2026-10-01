package etl_test

import (
	"fmt"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/etl"
)

func ExampleMetadataHeaderFormatter() {
	doc, err := document.NewDocument("Scope keeps document text provider-neutral.", nil)
	if err != nil {
		panic(err)
	}
	formatted, err := etl.NewMetadataHeaderFormatter(etl.MetadataHeaderFormatterConfig{}).Format(doc)
	if err != nil {
		panic(err)
	}

	fmt.Println(formatted)
	// Output:
	// Scope keeps document text provider-neutral.
}
