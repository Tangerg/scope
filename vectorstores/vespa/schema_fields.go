package vespa

import "fmt"

const (
	namespaceField       = "scope_namespace"
	metadataField        = "scope_metadata"
	metadataPathsField   = "scope_metadata_paths"
	nativeIDField        = "documentid"
	schemaNameField      = "sddocname"
	summaryFeaturesField = "summaryfeatures"
	matchFeaturesField   = "matchfeatures"
)

type schemaFields struct {
	content   string
	embedding string
}

func (s schemaFields) validate() error {
	for _, field := range []string{s.content, s.embedding} {
		if isSystemField(field) {
			return fmt.Errorf("vespa: field %q is reserved", field)
		}
	}
	if s.content == s.embedding {
		return fmt.Errorf("vespa: ContentField and EmbeddingField must differ, got %q", s.content)
	}
	if err := identifier(s.content).validate("ContentField"); err != nil {
		return err
	}
	return identifier(s.embedding).validate("EmbeddingField")
}

func (s schemaFields) reserved(field string) bool {
	return field == s.content || field == s.embedding || isSystemField(field)
}

func isSystemField(field string) bool {
	switch field {
	case namespaceField, metadataField, metadataPathsField, nativeIDField, schemaNameField, summaryFeaturesField, matchFeaturesField:
		return true
	}
	return false
}
