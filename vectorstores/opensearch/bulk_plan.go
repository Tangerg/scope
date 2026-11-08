package opensearch

import (
	jsonv2 "encoding/json/v2"
	"fmt"
)

// Small delete actions can produce much larger acknowledgments. A separate
// action limit bounds that amplification even with a generous request budget.
const bulkPublicationActionLimit = 512

// A plan admits every complete action before publication, so a late oversized
// action cannot leave an earlier mutation prefix applied.
type bulkPlan struct {
	maxBytes     int
	publications []bulkPublication
}

func (b *bulkPlan) addIndex(target bulkActionTarget, doc storedDocument) error {
	body, err := appendBulkLine(nil, bulkAction{Index: &target})
	if err != nil {
		return err
	}
	body, err = appendBulkLine(body, doc)
	if err != nil {
		return err
	}
	return b.add(target.ID, body)
}

func (b *bulkPlan) addDelete(target bulkActionTarget) error {
	body, err := appendBulkLine(nil, bulkAction{Delete: &target})
	if err != nil {
		return err
	}
	return b.add(target.ID, body)
}

func (b *bulkPlan) add(id string, body []byte) error {
	if len(body) > b.maxBytes {
		return fmt.Errorf("opensearch: encoded action for %q requires %d bytes; MaxBulkBytes is %d", id, len(body), b.maxBytes)
	}
	if len(b.publications) == 0 {
		b.publications = append(b.publications, bulkPublication{})
	}
	publication := &b.publications[len(b.publications)-1]
	if len(publication.ids) == bulkPublicationActionLimit || len(body) > b.maxBytes-len(publication.body) {
		b.publications = append(b.publications, bulkPublication{})
		publication = &b.publications[len(b.publications)-1]
	}
	publication.body = append(publication.body, body...)
	publication.ids = append(publication.ids, id)
	return nil
}

type bulkPublication struct {
	body []byte
	ids  []string
}

func appendBulkLine(body []byte, value any) ([]byte, error) {
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		return nil, err
	}
	body = append(body, encoded...)
	return append(body, '\n'), nil
}
