package content_test

import (
	"fmt"

	"github.com/Tangerg/scope/core/content"
)

func Example() {
	block := content.Block{Kind: content.Text, Text: "Report", Citations: []content.Citation{{Source: content.CitationSource{Kind: content.CitationSourceReference, Value: "report:2026"}}}}
	copy := block.Clone()
	copy.Citations[0].Source.Value = "another report"
	fmt.Println(block.Validate(), block.Citations[0].Source.Value)
	// Output: <nil> report:2026
}
