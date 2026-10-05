package vespa

import "fmt"

const (
	metadataField     = "scope_metadata"
	identityAttribute = "scope_documentid"
	nativeIDField     = "documentid"
	defaultSummary    = "default"
)

type schemaFields struct{ content, embedding string }

func (s schemaFields) validate() error {
	if s.content == s.embedding {
		return fmt.Errorf("vespa: content and embedding fields must differ")
	}
	for _, name := range []string{s.content, s.embedding} {
		switch name {
		case metadataField, identityAttribute, nativeIDField, "sddocname", "summaryfeatures", "matchfeatures":
			return fmt.Errorf("vespa: field %q is reserved", name)
		}
		if err := identifier(name).validate("field"); err != nil {
			return err
		}
	}
	return nil
}
