package cosmosdb

import (
	jsonv2 "encoding/json/v2"
	"errors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"

	"github.com/Tangerg/scope/core/chat"
)

func decodeMessages(raw []byte) ([]chat.Message, error) {
	var messages []chat.Message
	if err := jsonv2.Unmarshal(raw, &messages); err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, errors.New("stored append has no messages")
	}
	return messages, nil
}

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
