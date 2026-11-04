package azureaisearch

import "net/http"

type indexingOperation string

const (
	actionUpload indexingOperation = "upload"
	actionDelete indexingOperation = "delete"
)

func (i indexingOperation) acknowledges(statusCode int) bool {
	switch i {
	case actionUpload:
		return statusCode == http.StatusOK || statusCode == http.StatusCreated
	case actionDelete:
		return statusCode == http.StatusOK
	default:
		return false
	}
}

type indexingAction struct {
	operation indexingOperation
	id        string
	fields    map[string]any
}

func (i indexingAction) wire(idField string) map[string]any {
	wire := map[string]any{searchActionField: i.operation, idField: i.id}
	for field, value := range i.fields {
		wire[field] = value
	}
	return wire
}

func actionIDs(actions []indexingAction) []string {
	ids := make([]string, len(actions))
	for i, action := range actions {
		ids[i] = action.id
	}
	return ids
}
