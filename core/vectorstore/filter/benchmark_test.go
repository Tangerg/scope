package filter_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/vectorstore/filter"
)

func BenchmarkParse(b *testing.B) {
	benchmarks := []struct {
		name   string
		source string
	}{
		{name: "comparison", source: `category == 'tech'`},
		{name: "compound", source: `(category == 'tech' and year >= 2024) or title like 'Go%'`},
	}

	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := filter.Parse(benchmark.source); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// longConjunction builds a flat chain of n distinct comparisons joined by AND.
// Parse normalizes it, so this exercises the optimizer's flatten-and-dedup path
// on the left-deep tree the parser produces.
func longConjunction(n int) string {
	var source strings.Builder
	for index := range n {
		if index != 0 {
			source.WriteString(" and ")
		}
		source.WriteString("k")
		source.WriteString(strconv.Itoa(index))
		source.WriteString(" == ")
		source.WriteString(strconv.Itoa(index))
	}
	return source.String()
}

// BenchmarkParseConjunctionChain measures the normalization cost against chain
// length; the per-atom cost reveals whether that normalization grows faster
// than linearly in the number of operands.
func BenchmarkParseConjunctionChain(b *testing.B) {
	for _, n := range []int{250, 500, 1000} {
		source := longConjunction(n)
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := filter.Parse(source); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
