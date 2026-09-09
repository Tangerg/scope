package opensearch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

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

// dynamicTemplate names one dynamic-mapping rule. Metadata keys are unknown at
// index-creation time, so their fields have to be mapped dynamically; the
// default for a JSON string is "text with a .keyword sub-field", and the text
// field is analyzed. Filters compare whole values, so the metadata path maps
// strings straight to keyword instead, which also avoids the sub-field's
// ignore_above cutoff.
type dynamicTemplate struct {
	PathMatch        string         `json:"path_match"`
	MatchMappingType string         `json:"match_mapping_type"`
	Mapping          map[string]any `json:"mapping"`
}

// metadataKeywordTemplate keeps string metadata exactly comparable. Without it
// `metadata.author:"Alice"` reaches the analyzed text field and matches an
// author of "Alice Smith" or "alice", which is neither whole-value nor
// case-sensitive and disagrees with filter.Match.
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
	Index  *bulkActionTarget `json:"index,omitempty"`
	Delete *bulkActionTarget `json:"delete,omitempty"`
}

type bulkActionTarget struct {
	Index string `json:"_index,omitempty"`
	ID    string `json:"_id"`
}

type queryString struct {
	Query string `json:"query"`
}

type queryClause struct {
	QueryString queryString `json:"query_string"`
}

type nearestNeighbor struct {
	Vector []float32    `json:"vector"`
	K      int          `json:"k"`
	Filter *queryClause `json:"filter,omitempty"`
}

type nearestNeighborQuery struct {
	KNN map[string]nearestNeighbor `json:"knn"`
}

type searchRequest struct {
	Size  int                  `json:"size"`
	Query nearestNeighborQuery `json:"query"`
}

type deleteByQueryRequest struct {
	Query queryClause `json:"query"`
}

type bulkOutcome struct {
	operation bulkOperation
	response  *opensearchapi.BulkResp
}

func (b bulkOutcome) Err() error {
	if b.response == nil {
		return fmt.Errorf("opensearch: bulk %s returned no response", b.operation)
	}
	if !b.response.Errors {
		return nil
	}
	for _, item := range b.response.Items {
		for _, info := range item {
			if info.Error != nil {
				reason := info.Error.Reason
				if reason == "" {
					reason = "provider returned no reason"
				}
				return fmt.Errorf("opensearch: bulk %s failed for document %q with status %d: %s",
					b.operation, info.ID, info.Status, reason)
			}
		}
	}
	return fmt.Errorf("opensearch: bulk %s reported errors without an item failure", b.operation)
}

func encodeJSONRequest(value any) (io.Reader, error) {
	buf, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("opensearch: encode request: %w", err)
	}
	return bytes.NewReader(buf), nil
}
