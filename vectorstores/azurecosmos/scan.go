package azurecosmos

import (
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"

	"github.com/Tangerg/scope/core/document"
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

type selectedItem struct {
	doc  *document.Document
	etag azcore.ETag
}
