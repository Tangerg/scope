package elastic_test

import (
	"fmt"

	"github.com/Tangerg/scope/vectorstores/protocol/elastic"
)

func ExampleNewBulkPlan() {
	plan, err := elastic.NewBulkPlan(elastic.BulkConfig{
		Index: "documents", Operation: elastic.Index, MaxBytes: 1 << 20,
	})
	if err != nil {
		panic(err)
	}
	source := struct {
		Text string `json:"text"`
	}{Text: "Searchable content"}
	if err := plan.Index("document-1", source); err != nil {
		panic(err)
	}
	fmt.Println("complete document admitted")
	// Output: complete document admitted
}
