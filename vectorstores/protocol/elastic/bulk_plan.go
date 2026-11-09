package elastic

import (
	"cmp"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	DefaultMaxBulkBytes = 5_000_000
	DefaultIndexName    = "scope-vector-index"
	// Small delete actions otherwise amplify a bounded request into an unbounded receipt.
	bulkPublicationActionLimit = 512
)

type Operation string

const (
	Index  Operation = "index"
	Delete Operation = "delete"
)

type BulkConfig struct {
	Index        string
	Operation    Operation
	MaxBytes     int
	AllowMissing bool
}

func (b BulkConfig) Validate() error {
	if err := indexName(b.Index).validate(); err != nil {
		return err
	}
	if b.Operation != Index && b.Operation != Delete {
		return errors.New("elastic: unsupported bulk operation")
	}
	if b.AllowMissing && b.Operation != Delete {
		return errors.New("elastic: only identity deletion may allow missing records")
	}
	if b.MaxBytes < 0 {
		return errors.New("elastic: negative MaxBulkBytes")
	}
	return nil
}

// BulkPlan admits every complete action before publication, so a late oversized
// record cannot leave an earlier mutation prefix applied. Plans have one operation.
type BulkPlan struct {
	config       BulkConfig
	publications []bulkPublication
	ids          map[string]struct{}
}

func NewBulkPlan(config BulkConfig) (*BulkPlan, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	config.MaxBytes = cmp.Or(config.MaxBytes, DefaultMaxBulkBytes)
	return &BulkPlan{config: config, ids: make(map[string]struct{})}, nil
}

func (b *BulkPlan) Index(id string, doc any) error {
	if b == nil || b.config.Operation != Index {
		return errors.New("elastic: index action requires an index plan")
	}
	target := bulkActionTarget{Index: b.config.Index, ID: id}
	body, err := appendBulkLine(nil, bulkAction{Index: &target})
	if err != nil {
		return err
	}
	body, err = appendBulkLine(body, doc)
	if err != nil {
		return err
	}
	return b.add(id, body)
}

func (b *BulkPlan) Delete(id string, version *Version) error {
	if b == nil || b.config.Operation != Delete {
		return errors.New("elastic: delete action requires a delete plan")
	}
	target := bulkActionTarget{Index: b.config.Index, ID: id}
	if version != nil {
		if b.config.AllowMissing {
			return errors.New("elastic: conditional deletion cannot allow missing records")
		}
		if err := version.Validate(); err != nil {
			return err
		}
		target.SeqNo, target.PrimaryTerm = &version.SeqNo, &version.PrimaryTerm
	}
	body, err := appendBulkLine(nil, bulkAction{Delete: &target})
	if err != nil {
		return err
	}
	return b.add(id, body)
}

func (b *BulkPlan) add(id string, body []byte) error {
	if err := DocumentID(id).Validate(); err != nil {
		return err
	}
	if _, duplicate := b.ids[id]; duplicate {
		return vectorstore.ErrDuplicateDocumentID
	}
	if len(body) > b.config.MaxBytes {
		return fmt.Errorf("elastic: encoded action for %q requires %d bytes; MaxBulkBytes is %d", id, len(body), b.config.MaxBytes)
	}
	if len(b.publications) == 0 {
		b.publications = append(b.publications, bulkPublication{})
	}
	publication := &b.publications[len(b.publications)-1]
	if len(publication.ids) == bulkPublicationActionLimit || len(body) > b.config.MaxBytes-len(publication.body) {
		b.publications = append(b.publications, bulkPublication{})
		publication = &b.publications[len(b.publications)-1]
	}
	publication.body = append(publication.body, body...)
	publication.ids = append(publication.ids, id)
	b.ids[id] = struct{}{}
	return nil
}

func (b *BulkPlan) Publish(ctx context.Context, client *Client) error {
	if b == nil {
		return errors.New("elastic: missing bulk plan")
	}
	if err := b.config.Validate(); err != nil {
		return err
	}
	for i, publication := range b.publications {
		if err := b.publish(ctx, client, publication); err != nil {
			if b.config.Operation != Index {
				return err
			}
			outcome, cause := vectorstore.WriteOutcome{Unconfirmed: publication.ids}, err
			if w, ok := errors.AsType[*vectorstore.WriteError](err); ok {
				outcome, cause = w.Outcome(), w.Unwrap()
			}
			for _, prior := range b.publications[:i] {
				outcome.Acknowledged = append(outcome.Acknowledged, prior.ids...)
			}
			for _, later := range b.publications[i+1:] {
				outcome.Unattempted = append(outcome.Unattempted, later.ids...)
			}
			return vectorstore.NewWriteError(cause, outcome)
		}
	}
	return nil
}

func (b *BulkPlan) publish(ctx context.Context, client *Client, publication bulkPublication) error {
	raw, readErr := client.Read(ctx, http.MethodPost, bulkPath, nil, publication.body, 0)
	_, invalidStatus := errors.AsType[*httpclient.ResponseError](readErr)
	if invalidStatus {
		return readErr
	}
	var response bulkResponse
	decodeErr := jsonv2.Unmarshal(raw, &response)
	cause := errors.Join(readErr, decodeErr)
	if decodeErr != nil && !errors.Is(decodeErr, io.ErrUnexpectedEOF) {
		return cause
	}
	cause = errors.Join(cause, response.validate(b.config.Operation, b.config.Index, publication.ids, b.config.AllowMissing))
	if cause == nil || b.config.Operation != Index {
		return cause
	}
	return response.writeFailure(b.config.Index, publication.ids, cause)
}

type bulkPublication struct {
	body []byte
	ids  []string
}

type bulkAction struct {
	Index  *bulkActionTarget `json:"index,omitzero"`
	Delete *bulkActionTarget `json:"delete,omitzero"`
}

type bulkActionTarget struct {
	Index       string `json:"_index"`
	ID          string `json:"_id"`
	SeqNo       *int64 `json:"if_seq_no,omitzero"`
	PrimaryTerm *int64 `json:"if_primary_term,omitzero"`
}

func appendBulkLine(body []byte, value any) ([]byte, error) {
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		return nil, err
	}
	body = append(body, encoded...)
	return append(body, '\n'), nil
}
