package redis

import (
	"strings"
	"testing"
)

// Every configured field name reaches the query language: FT.CREATE declares it
// and the visitor emits it as `@name`. RediSearch cannot quote a field name, so
// a name carrying its syntax would be read as syntax.
func TestFieldIdentifierValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "identifier", value: "author"},
		{name: "underscored", value: "_author_2"},
		// A RediSearch schema is flat, so a nested metadata key is declared as
		// a dotted field name. Rejecting the dot would reject the only way to
		// filter one.
		{name: "dotted path", value: "profile.author"},
		{name: "empty", value: "", wantErr: true},
		{name: "tag clause escape", value: "a}|@b", wantErr: true},
		{name: "space", value: "a b", wantErr: true},
		{name: "empty segment", value: "profile..author", wantErr: true},
		{name: "leading digit", value: "1author", wantErr: true},
		{name: "hyphen", value: "a-b", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := fieldIdentifier(test.value).validate("Field")
			if test.wantErr {
				if err == nil {
					t.Fatalf("validate(%q) = nil error, want a rejection", test.value)
				}
				if !strings.Contains(err.Error(), "Field") {
					t.Fatalf("validate(%q) = %v, want the field named", test.value, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate(%q) = %v, want nil", test.value, err)
			}
		})
	}
}

// The check runs where the misconfiguration is, so a name that cannot be
// written into a query fails construction rather than producing a query nobody
// meant.
func TestConfigRefusesUnnameableFieldNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*StoreConfig)
		field  string
	}{
		{
			name:   "content field",
			mutate: func(c *StoreConfig) { c.ContentField = "a}|@b" },
			field:  "ContentField",
		},
		{
			name:   "embedding field",
			mutate: func(c *StoreConfig) { c.EmbeddingField = "a b" },
			field:  "EmbeddingField",
		},
		{
			name:   "metadata field",
			mutate: func(c *StoreConfig) { c.MetadataJSONField = "a-b" },
			field:  "MetadataJSONField",
		},
		{
			name: "declared metadata field",
			mutate: func(c *StoreConfig) {
				c.MetadataFields = []MetadataField{{Name: "a}|@b", Type: FieldTag}}
			},
			field: "MetadataFields[0].Name",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := StoreConfig{}
			config.applyDefaults()
			test.mutate(&config)

			err := config.validateFieldIdentifiers()
			if err == nil {
				t.Fatal("validateFieldIdentifiers() = nil error, want a rejection")
			}
			if !strings.Contains(err.Error(), test.field) {
				t.Fatalf("validateFieldIdentifiers() = %v, want %s named", err, test.field)
			}
		})
	}

	t.Run("defaults are nameable", func(t *testing.T) {
		config := StoreConfig{}
		config.applyDefaults()
		if err := config.validateFieldIdentifiers(); err != nil {
			t.Fatalf("validateFieldIdentifiers(defaults) = %v, want nil", err)
		}
	})
}
