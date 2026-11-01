package chroma

import (
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// ScanConfig explicitly grants a bounded source read. It never enables a
// fallback from Query, including when Filter cannot be lowered natively.
type ScanConfig struct {
	Budget vectorstore.ScanBudget
	Filter filter.Predicate
}

func (s ScanConfig) Validate() error {
	if err := s.Budget.Validate(); err != nil {
		return err
	}
	if s.Filter != nil {
		return s.Filter.Validate()
	}
	return nil
}
