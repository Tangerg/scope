package bedrockkb

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/vectorstore"
)

// StoreConfig binds the native Retrieve REST boundary. The host owns HTTP
// authentication, retries, timeouts and lifecycle; ingestion stays in Bedrock.
type StoreConfig struct {
	Endpoint string
	// HTTPClient settings are captured; the host retains its transport and jar.
	// Requests remain within Endpoint's origin even when redirects are followed.
	HTTPClient                     *http.Client
	KnowledgeBaseID                string
	MaxResponseBytes               int64
	RerankingModelConfiguration    *types.VectorSearchBedrockRerankingModelConfiguration
	RerankingMetadataConfiguration *types.MetadataConfigurationForReranking
	ImplicitFilterConfiguration    *types.ImplicitFilterConfiguration
}

func (s StoreConfig) Validate() error {
	endpoint, err := url.Parse(s.Endpoint)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || strings.ContainsAny(s.Endpoint, "?#") || strings.Trim(endpoint.Path, "/") != "" {
		return errors.New("bedrockkb: Endpoint must be an HTTP service origin")
	}
	if s.HTTPClient == nil {
		return errors.New("bedrockkb: HTTPClient is required")
	}
	if strings.TrimSpace(s.KnowledgeBaseID) == "" {
		return errors.New("bedrockkb: KnowledgeBaseID is required")
	}
	if s.MaxResponseBytes < 0 || s.MaxResponseBytes == math.MaxInt64 {
		return errors.New("bedrockkb: MaxResponseBytes must be nonnegative and below MaxInt64")
	}
	if s.RerankingMetadataConfiguration != nil && s.RerankingModelConfiguration == nil {
		return errors.New("bedrockkb: reranking metadata requires a reranking model")
	}
	return nil
}

func (s StoreConfig) nativePolicy() (nativeConfiguration, error) {
	configuration := nativeConfiguration{}
	if s.RerankingModelConfiguration != nil {
		model, err := newNativeRerankingModel(s.RerankingModelConfiguration)
		if err != nil {
			return nativeConfiguration{}, err
		}
		selection, err := newNativeRerankingMetadata(s.RerankingMetadataConfiguration)
		if err != nil {
			return nativeConfiguration{}, err
		}
		configuration.Reranking = &nativeRerankingConfiguration{
			Type:    types.VectorSearchRerankingConfigurationTypeBedrockRerankingModel,
			Bedrock: nativeBedrockReranking{Model: model, Metadata: selection},
		}
	}
	implicit, err := newNativeImplicitFilter(s.ImplicitFilterConfiguration)
	if err != nil {
		return nativeConfiguration{}, err
	}
	configuration.Implicit = implicit
	return configuration, nil
}

type nativeConfiguration struct {
	Count     int                           `json:"numberOfResults"`
	Mode      types.SearchType              `json:"overrideSearchType"`
	Reranking *nativeRerankingConfiguration `json:"rerankingConfiguration,omitzero"`
	Implicit  *nativeImplicitFilter         `json:"implicitFilterConfiguration,omitzero"`
}

func (n nativeConfiguration) forOptions(options vectorstore.SearchOptions) nativeConfiguration {
	n.Count = options.ResultLimit()
	n.Mode = types.SearchTypeSemantic
	if options.EffectiveMode() == vectorstore.SearchModeHybrid {
		n.Mode = types.SearchTypeHybrid
	}
	if n.Reranking != nil {
		reranking := *n.Reranking
		reranking.Bedrock.Count = options.ResultLimit()
		n.Reranking = &reranking
	}
	return n
}

type nativeRerankingConfiguration struct {
	Type    types.VectorSearchRerankingConfigurationType `json:"type"`
	Bedrock nativeBedrockReranking                       `json:"bedrockRerankingConfiguration"`
}

type nativeBedrockReranking struct {
	Model    nativeRerankingModel     `json:"modelConfiguration"`
	Metadata *nativeRerankingMetadata `json:"metadataConfiguration,omitzero"`
	Count    int                      `json:"numberOfRerankedResults"`
}

type nativeRerankingModel struct {
	ARN    string       `json:"modelArn"`
	Fields metadata.Map `json:"additionalModelRequestFields,omitzero"`
}

func newNativeRerankingModel(config *types.VectorSearchBedrockRerankingModelConfiguration) (nativeRerankingModel, error) {
	if config.ModelArn == nil || strings.TrimSpace(*config.ModelArn) == "" {
		return nativeRerankingModel{}, errors.New("bedrockkb: reranking model ARN is required")
	}
	var fields metadata.Map
	if len(config.AdditionalModelRequestFields) != 0 {
		fields = make(metadata.Map, len(config.AdditionalModelRequestFields))
	}
	for key, value := range config.AdditionalModelRequestFields {
		if lo.IsNil(value) {
			return nativeRerankingModel{}, fmt.Errorf("bedrockkb: nil reranking model field %q", key)
		}
		raw, err := value.MarshalSmithyDocument()
		if err != nil {
			return nativeRerankingModel{}, err
		}
		fields[key] = raw
	}
	if err := fields.Validate(); err != nil {
		return nativeRerankingModel{}, err
	}
	return nativeRerankingModel{ARN: *config.ModelArn, Fields: fields.Clone()}, nil
}

type nativeRerankingMetadata struct {
	Selection types.RerankingMetadataSelectionMode `json:"selectionMode"`
	Selective *nativeRerankingSelection            `json:"selectiveModeConfiguration,omitzero"`
}

func newNativeRerankingMetadata(config *types.MetadataConfigurationForReranking) (*nativeRerankingMetadata, error) {
	if config == nil {
		return nil, nil
	}
	selection := &nativeRerankingMetadata{Selection: config.SelectionMode}
	switch config.SelectionMode {
	case types.RerankingMetadataSelectionModeAll:
		if !lo.IsNil(config.SelectiveModeConfiguration) {
			return nil, errors.New("bedrockkb: ALL reranking metadata cannot carry selective fields")
		}
	case types.RerankingMetadataSelectionModeSelective:
		selective, err := newNativeRerankingSelection(config.SelectiveModeConfiguration)
		if err != nil {
			return nil, err
		}
		selection.Selective = &selective
	default:
		return nil, errors.New("bedrockkb: unsupported native reranking metadata selection mode")
	}
	return selection, nil
}

type nativeRerankingSelection struct {
	Included []nativeRerankingField `json:"fieldsToInclude,omitzero"`
	Excluded []nativeRerankingField `json:"fieldsToExclude,omitzero"`
}

func newNativeRerankingSelection(config types.RerankingMetadataSelectiveModeConfiguration) (nativeRerankingSelection, error) {
	var fields []types.FieldForReranking
	included := false
	switch subset := config.(type) {
	case *types.RerankingMetadataSelectiveModeConfigurationMemberFieldsToInclude:
		if subset != nil {
			fields = subset.Value
			included = true
		}
	case *types.RerankingMetadataSelectiveModeConfigurationMemberFieldsToExclude:
		if subset != nil {
			fields = subset.Value
		}
	}
	if len(fields) == 0 {
		return nativeRerankingSelection{}, errors.New("bedrockkb: SELECTIVE reranking metadata requires native fields")
	}
	projected := make([]nativeRerankingField, len(fields))
	for index, field := range fields {
		if field.FieldName == nil || *field.FieldName == "" {
			return nativeRerankingSelection{}, errors.New("bedrockkb: reranking metadata field name is required")
		}
		projected[index] = nativeRerankingField{Name: *field.FieldName}
	}
	if included {
		return nativeRerankingSelection{Included: projected}, nil
	}
	return nativeRerankingSelection{Excluded: projected}, nil
}

type nativeRerankingField struct {
	Name string `json:"fieldName"`
}

type nativeImplicitFilter struct {
	ARN        string                    `json:"modelArn"`
	Attributes []nativeMetadataAttribute `json:"metadataAttributes"`
}

func newNativeImplicitFilter(config *types.ImplicitFilterConfiguration) (*nativeImplicitFilter, error) {
	if config == nil {
		return nil, nil
	}
	if config.ModelArn == nil || *config.ModelArn == "" {
		return nil, errors.New("bedrockkb: implicit filter model ARN is required")
	}
	attributes := make([]nativeMetadataAttribute, len(config.MetadataAttributes))
	for index, attribute := range config.MetadataAttributes {
		attributes[index].Type = attribute.Type
		if attribute.Key != nil {
			attributes[index].Key = new(*attribute.Key)
		}
		if attribute.Description != nil {
			attributes[index].Description = new(*attribute.Description)
		}
	}
	return &nativeImplicitFilter{ARN: *config.ModelArn, Attributes: attributes}, nil
}

type nativeMetadataAttribute struct {
	Key         *string             `json:"key"`
	Type        types.AttributeType `json:"type"`
	Description *string             `json:"description"`
}
