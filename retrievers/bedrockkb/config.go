package bedrockkb

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"
	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/metadata"
)

const (
	DefaultLimit                      = 5
	DefaultPageLimit                  = 8
	maximumResults                    = 100
	maximumKnowledgeBaseIdentityBytes = 2048
)

var knowledgeBaseIdentity = regexp.MustCompile(`^(?:[0-9a-zA-Z]{10}|arn:aws(?:-[a-z0-9-]+)?:bedrock:[a-z0-9-]{1,20}:[0-9]{12}:knowledge-base/[0-9a-zA-Z]{10})$`)

// MetadataScope requires a single exact native string value on every returned source.
type MetadataScope struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (m MetadataScope) validate() error {
	if strings.TrimSpace(m.Key) == "" || m.Value == "" {
		return errors.New("bedrockkb: exact metadata scope requires a key and value")
	}
	return nil
}

// Config binds the native Retrieve REST boundary. The host owns HTTP
// authentication, retries, timeouts and lifecycle; ingestion stays in Bedrock.
type Config struct {
	// Zero limits use DefaultLimit and DefaultPageLimit; PageLimit bounds service calls.
	Limit     int
	PageLimit int
	// Mode defaults to SEMANTIC.
	Mode types.SearchType
	// Scope binds authorization to this retriever. Query metadata cannot replace it.
	Scope *MetadataScope
	// UserID binds Bedrock userContext.userId for access control filtering on
	// knowledge bases configured with native document ACLs.
	UserID   string
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

func (c Config) Validate() error {
	if err := validateEndpoint(c.Endpoint); err != nil {
		return err
	}
	if c.HTTPClient == nil {
		return errors.New("bedrockkb: HTTPClient is required")
	}
	if !knowledgeBaseIdentity.MatchString(c.KnowledgeBaseID) || len(c.KnowledgeBaseID) > maximumKnowledgeBaseIdentityBytes {
		return errors.New("bedrockkb: invalid knowledge base ID or ARN")
	}
	if c.UserID != "" && (!utf8.ValidString(c.UserID) || strings.TrimSpace(c.UserID) == "") {
		return errors.New("bedrockkb: invalid user identity")
	}
	if c.MaxResponseBytes < 0 || c.MaxResponseBytes == math.MaxInt64 {
		return errors.New("bedrockkb: MaxResponseBytes must be nonnegative and below MaxInt64")
	}
	return c.validateRetrieval()
}

func validateEndpoint(raw string) error {
	endpoint, err := url.Parse(raw)
	if err != nil || strings.ContainsAny(raw, "?#") || strings.Trim(endpoint.Path, "/") != "" {
		return errors.New("bedrockkb: Endpoint must be an HTTP service origin")
	}
	if _, err := httpclient.NewOrigin(endpoint); err != nil {
		return errors.New("bedrockkb: Endpoint must be an HTTP service origin")
	}
	return nil
}

// validateRetrieval checks the native retrieval policy: limits, search mode,
// metadata scope and reranking.
func (c Config) validateRetrieval() error {
	if c.Limit < 0 || c.Limit > maximumResults || c.PageLimit < 0 {
		return errors.New("bedrockkb: invalid retrieval limits")
	}
	if c.Mode != "" && c.Mode != types.SearchTypeSemantic && c.Mode != types.SearchTypeHybrid {
		return errors.New("bedrockkb: invalid native search mode")
	}
	if c.Scope != nil {
		if err := c.Scope.validate(); err != nil {
			return err
		}
	}
	if c.RerankingMetadataConfiguration != nil && c.RerankingModelConfiguration == nil {
		return errors.New("bedrockkb: reranking metadata requires a reranking model")
	}
	return nil
}

func (c Config) nativePolicy() (nativeConfiguration, error) {
	configuration := nativeConfiguration{Count: cmp.Or(c.Limit, DefaultLimit), Mode: cmp.Or(c.Mode, types.SearchTypeSemantic)}
	if c.Scope != nil {
		configuration.Filter = &nativeFilter{Equals: *c.Scope}
	}
	if c.RerankingModelConfiguration != nil {
		model, err := newNativeRerankingModel(c.RerankingModelConfiguration)
		if err != nil {
			return nativeConfiguration{}, err
		}
		selection, err := newNativeRerankingMetadata(c.RerankingMetadataConfiguration)
		if err != nil {
			return nativeConfiguration{}, err
		}
		configuration.Reranking = &nativeRerankingConfiguration{
			Type:    types.VectorSearchRerankingConfigurationTypeBedrockRerankingModel,
			Bedrock: nativeBedrockReranking{Model: model, Metadata: selection, Count: configuration.Count},
		}
	}
	implicit, err := newNativeImplicitFilter(c.ImplicitFilterConfiguration)
	if err != nil {
		return nativeConfiguration{}, err
	}
	configuration.Implicit = implicit
	return configuration, nil
}

type nativeFilter struct {
	Equals MetadataScope `json:"equals"`
}

type nativeConfiguration struct {
	Filter    *nativeFilter                 `json:"filter,omitzero"`
	Count     int                           `json:"numberOfResults"`
	Mode      types.SearchType              `json:"overrideSearchType"`
	Reranking *nativeRerankingConfiguration `json:"rerankingConfiguration,omitzero"`
	Implicit  *nativeImplicitFilter         `json:"implicitFilterConfiguration,omitzero"`
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

const (
	maximumImplicitModelReferenceCharacters       = 2048
	maximumImplicitMetadataAttributes             = 25
	maximumMetadataAttributeKeyCharacters         = 256
	maximumMetadataAttributeDescriptionCharacters = 1024
)

var implicitModelReferencePattern = regexp.MustCompile(`^(arn:aws(-[^:]+)?:(bedrock|sagemaker):[a-z0-9-]{1,20}:([0-9]{12})?:([a-z-]+/)?)?([a-z0-9.-]{1,63}){0,2}(([:][a-z0-9-]{1,63}){0,2})?(/[a-z0-9]{1,12})?$`)

func newNativeImplicitFilter(config *types.ImplicitFilterConfiguration) (*nativeImplicitFilter, error) {
	if config == nil {
		return nil, nil
	}
	if config.ModelArn == nil || *config.ModelArn == "" || !utf8.ValidString(*config.ModelArn) || utf8.RuneCountInString(*config.ModelArn) > maximumImplicitModelReferenceCharacters || !implicitModelReferencePattern.MatchString(*config.ModelArn) {
		return nil, errors.New("bedrockkb: invalid implicit filter model reference")
	}
	if len(config.MetadataAttributes) == 0 || len(config.MetadataAttributes) > maximumImplicitMetadataAttributes {
		return nil, fmt.Errorf("bedrockkb: implicit filtering requires 1..%d metadata attributes", maximumImplicitMetadataAttributes)
	}
	attributes := make([]nativeMetadataAttribute, len(config.MetadataAttributes))
	for index, attribute := range config.MetadataAttributes {
		projected, err := newNativeMetadataAttribute(attribute)
		if err != nil {
			return nil, fmt.Errorf("bedrockkb: implicit metadata attribute %d: %w", index, err)
		}
		attributes[index] = projected
	}
	return &nativeImplicitFilter{ARN: *config.ModelArn, Attributes: attributes}, nil
}

type nativeMetadataAttribute struct {
	Key         string              `json:"key"`
	Type        types.AttributeType `json:"type"`
	Description string              `json:"description"`
}

func newNativeMetadataAttribute(attribute types.MetadataAttributeSchema) (nativeMetadataAttribute, error) {
	if attribute.Key == nil || *attribute.Key == "" || !utf8.ValidString(*attribute.Key) || utf8.RuneCountInString(*attribute.Key) > maximumMetadataAttributeKeyCharacters {
		return nativeMetadataAttribute{}, fmt.Errorf("key must contain 1..%d UTF-8 characters", maximumMetadataAttributeKeyCharacters)
	}
	if attribute.Description == nil || *attribute.Description == "" || !utf8.ValidString(*attribute.Description) || utf8.RuneCountInString(*attribute.Description) > maximumMetadataAttributeDescriptionCharacters {
		return nativeMetadataAttribute{}, fmt.Errorf("description must contain 1..%d UTF-8 characters", maximumMetadataAttributeDescriptionCharacters)
	}
	if !slices.Contains(attribute.Type.Values(), attribute.Type) {
		return nativeMetadataAttribute{}, errors.New("unsupported type")
	}
	return nativeMetadataAttribute{Key: *attribute.Key, Type: attribute.Type, Description: *attribute.Description}, nil
}
