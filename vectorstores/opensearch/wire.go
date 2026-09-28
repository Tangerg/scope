package opensearch

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

const (
	bulkRecordSeparator = '\n'
	mappingTypeText     = "text"
	mappingTypeVector   = "knn_vector"
	mappingTypeObject   = "object"
	mappingTypeKeyword  = "keyword"
)

type bulkOperation string

const (
	bulkOperationIndex  bulkOperation = "index"
	bulkOperationDelete bulkOperation = "delete"
)

type createIndexRequest struct {
	Settings indexSettings `json:"settings"`
	Mappings indexMappings `json:"mappings"`
}

type indexSettings struct {
	KNN bool `json:"index.knn"`
}

type indexMappings struct {
	DynamicTemplates []map[string]dynamicTemplate `json:"dynamic_templates,omitempty"`
	Properties       map[string]any               `json:"properties"`
}

// dynamicTemplate preserves the index creation policy for native metadata terms.
// Core predicates are evaluated from stored source, independently of these terms.
type dynamicTemplate struct {
	PathMatch        string         `json:"path_match"`
	MatchMappingType string         `json:"match_mapping_type"`
	Mapping          map[string]any `json:"mapping"`
}

func metadataKeywordTemplate(metadataField string) map[string]dynamicTemplate {
	return map[string]dynamicTemplate{
		"metadata_strings_are_keywords": {
			PathMatch:        metadataField + ".*",
			MatchMappingType: "string",
			Mapping:          map[string]any{"type": mappingTypeKeyword},
		},
	}
}

type textFieldMapping struct {
	Type string `json:"type"`
}

type vectorFieldMapping struct {
	Type       string           `json:"type"`
	Dimensions int              `json:"dimension"`
	Method     annMethodMapping `json:"method"`
}

type annMethodMapping struct {
	Name      string    `json:"name"`
	Engine    Engine    `json:"engine"`
	SpaceType SpaceType `json:"space_type"`
}

// storedVectorField is the part of an existing knn_vector mapping this store
// has to agree with. OpenSearch omits what was left at its default rather
// than echoing it, so an absent space type is resolved rather than read.
type storedVectorField struct {
	Type       string    `json:"type"`
	Dimensions int       `json:"dimension"`
	SpaceType  SpaceType `json:"space_type"`
	ModelID    string    `json:"model_id"`
	Method     *struct {
		SpaceType SpaceType `json:"space_type"`
		Engine    Engine    `json:"engine"`
	} `json:"method"`
}

// effectiveSpaceType resolves the space a field was built for. It is optional
// on the field, "can also be specified within the method", and "defaults to
// l2" when neither carries it. A field trained from a model carries neither,
// and its space belongs to the model rather than the mapping, so it is
// reported instead of defaulted.
func (s storedVectorField) effectiveSpaceType() (SpaceType, error) {
	if s.SpaceType != "" && s.Method != nil && s.Method.SpaceType != "" && s.SpaceType != s.Method.SpaceType {
		return "", fmt.Errorf("the field declares space type %q and its method declares %q",
			s.SpaceType, s.Method.SpaceType)
	}
	if s.SpaceType != "" {
		return s.SpaceType, nil
	}
	if s.Method != nil && s.Method.SpaceType != "" {
		return s.Method.SpaceType, nil
	}
	if s.ModelID != "" {
		return "", fmt.Errorf("it is trained from model %q, whose space type is not in the mapping", s.ModelID)
	}
	return SpaceTypeL2, nil
}

type objectFieldMapping struct {
	Type    string `json:"type"`
	Dynamic bool   `json:"dynamic"`
}

type bulkAction struct {
	Index  *bulkActionTarget `json:"index,omitzero"`
	Delete *bulkActionTarget `json:"delete,omitzero"`
}

type bulkActionTarget struct {
	Index       string `json:"_index,omitempty"`
	ID          string `json:"_id"`
	Routing     string `json:"routing,omitzero"`
	SeqNo       *int   `json:"if_seq_no,omitzero"`
	PrimaryTerm *int   `json:"if_primary_term,omitzero"`
}

type idsQuery struct {
	Values []string `json:"values"`
}
type queryClause struct {
	IDs idsQuery `json:"ids"`
}

type metadataScanRequest struct {
	Size             int      `json:"size"`
	Source           []string `json:"_source"`
	StoredFields     []string `json:"stored_fields"`
	Sort             []string `json:"sort"`
	SeqNoPrimaryTerm bool     `json:"seq_no_primary_term"`
}

type nearestNeighbor struct {
	Vector []float32    `json:"vector"`
	K      int          `json:"k"`
	Filter *queryClause `json:"filter,omitzero"`
}

type nearestNeighborQuery struct {
	KNN map[string]nearestNeighbor `json:"knn"`
}

type searchRequest struct {
	Size  int                  `json:"size"`
	Query nearestNeighborQuery `json:"query"`
}

type bulkOutcome struct {
	operation   bulkOperation
	response    *opensearchapi.BulkResp
	expectedIDs []string
}

func (b bulkOutcome) err() error {
	if b.response == nil {
		return fmt.Errorf("opensearch: bulk %s returned no response", b.operation)
	}
	if len(b.response.Items) != len(b.expectedIDs) {
		return fmt.Errorf("opensearch: bulk %s acknowledged %d of %d documents", b.operation, len(b.response.Items), len(b.expectedIDs))
	}
	for index, item := range b.response.Items {
		info, found := item[string(b.operation)]
		if !found || len(item) != 1 || info.ID != b.expectedIDs[index] {
			return fmt.Errorf("opensearch: bulk %s response item %d does not identify requested document %q", b.operation, index, b.expectedIDs[index])
		}
		success := info.Status >= 200 && info.Status < 300
		missing := b.operation == bulkOperationDelete && info.Status == 404
		if !success && !missing || info.Error != nil && !missing {
			reason := "provider returned no reason"
			if info.Error != nil && info.Error.Reason != "" {
				reason = info.Error.Reason
			}
			return fmt.Errorf("opensearch: bulk %s failed for document %q with status %d: %s", b.operation, info.ID, info.Status, reason)
		}
	}
	if b.response.Errors {
		return fmt.Errorf("opensearch: bulk %s reported an unexplained failure", b.operation)
	}
	return nil
}

func encodeJSONRequest(value any) (io.Reader, error) {
	buf, err := jsonv2.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("opensearch: encode request: %w", err)
	}
	return bytes.NewReader(buf), nil
}

type storedSource struct {
	Enabled  *bool    `json:"enabled"`
	Includes []string `json:"includes"`
	Excludes []string `json:"excludes"`
	Mode     string   `json:"mode"`
}

func (s storedSource) validate(fields ...string) error {
	if s.Enabled != nil && !*s.Enabled || s.Mode != "" && s.Mode != "stored" {
		return fmt.Errorf("%w: filtering requires stored _source", ErrIncompatibleIndex)
	}
	for _, field := range fields {
		if field == "" {
			continue
		}
		included := len(s.Includes) == 0
		for _, pattern := range s.Includes {
			if pattern == "*" || pattern == field {
				included = true
			}
		}
		if !included {
			return fmt.Errorf("%w: source includes must preserve complete field %q", ErrIncompatibleIndex, field)
		}
		for _, pattern := range s.Excludes {
			// A wildcard that can address this root may prune a descendant. Exact
			// exclusions of unrelated fields, such as embedding, remain acceptable.
			root := strings.SplitN(pattern, ".", 2)[0]
			match, err := path.Match(root, strings.SplitN(field, ".", 2)[0])
			if err != nil || match {
				return fmt.Errorf("%w: source exclusion %q may prune field %q", ErrIncompatibleIndex, pattern, field)
			}
		}
	}
	return nil
}
