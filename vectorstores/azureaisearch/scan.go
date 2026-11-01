package azureaisearch

import (
	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/core/vectorstore/filter"
)

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
