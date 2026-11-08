package cosmosdb

import (
	"errors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
)

func strongItemOptions() *azcosmos.ItemOptions {
	consistency := requiredConsistency
	return &azcosmos.ItemOptions{ConsistencyLevel: &consistency}
}

func responseStatus(err error) int {
	if response, ok := errors.AsType[*azcore.ResponseError](err); ok {
		return response.StatusCode
	}
	return 0
}
