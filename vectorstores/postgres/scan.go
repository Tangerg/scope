package postgres

import (
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

// ScanConfig authorizes an explicit full-table read. The Host must already
// have authority over the whole table. MaxBytes counts ID, text, stored JSON
// and float32 coordinates, not wire framing or database internal I/O.
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
