package azureaisearch

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/vectorstore"
)

const maximumHTTPErrorStatusCode = 599

type indexingAcknowledgment struct {
	Key        string `json:"key"`
	Status     *bool  `json:"status"`
	StatusCode int    `json:"statusCode"`
	Message    string `json:"errorMessage"`
}

func (i indexingAcknowledgment) record(action indexingAction, outcome *vectorstore.WriteOutcome) error {
	switch {
	case i.Status != nil && *i.Status && action.operation.acknowledges(i.StatusCode):
		outcome.Acknowledged = append(outcome.Acknowledged, action.id)
		return nil
	case i.Status != nil && !*i.Status && i.StatusCode >= http.StatusBadRequest && i.StatusCode <= maximumHTTPErrorStatusCode:
		outcome.Rejected = append(outcome.Rejected, action.id)
		return fmt.Errorf("azureaisearch: document %q: status=%d: %s", action.id, i.StatusCode, i.Message)
	default:
		outcome.Unconfirmed = append(outcome.Unconfirmed, action.id)
		return fmt.Errorf("azureaisearch: inconsistent status for %q", action.id)
	}
}

type indexingResponse struct {
	Value []indexingAcknowledgment `json:"value"`
}

func (i indexingResponse) evidence(actions []indexingAction) (vectorstore.WriteOutcome, error) {
	uncertain := func(cause error) (vectorstore.WriteOutcome, error) {
		return vectorstore.WriteOutcome{Unconfirmed: actionIDs(actions)}, cause
	}
	if len(i.Value) != len(actions) {
		return uncertain(errors.New("azureaisearch: incomplete write acknowledgment"))
	}
	keyed := make(map[string]indexingAcknowledgment, len(i.Value))
	for _, acknowledgment := range i.Value {
		if _, repeated := keyed[acknowledgment.Key]; repeated {
			return uncertain(fmt.Errorf("azureaisearch: duplicate acknowledgment %q", acknowledgment.Key))
		}
		keyed[acknowledgment.Key] = acknowledgment
	}
	// Establish a complete identity correspondence before classifying any row;
	// outcome slices never reuse or rewrite the requested identities' backing array.
	for _, action := range actions {
		if _, present := keyed[action.id]; !present {
			return uncertain(fmt.Errorf("azureaisearch: missing acknowledgment %q", action.id))
		}
	}
	var outcome vectorstore.WriteOutcome
	var cause error
	for _, action := range actions {
		cause = errors.Join(cause, keyed[action.id].record(action, &outcome))
	}
	return outcome, cause
}

func readIndexingEvidence(raw []byte, actions []indexingAction) (vectorstore.WriteOutcome, error) {
	var response indexingResponse
	if err := jsonv2.Unmarshal(raw, &response); err != nil {
		return vectorstore.WriteOutcome{Unconfirmed: actionIDs(actions)}, err
	}
	return response.evidence(actions)
}
