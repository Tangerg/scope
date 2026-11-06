package mcp

// Keyword roles are confined to schema positions. Property names and literal
// instances may themselves contain "$id" or "$ref" without changing scope.
type schemaKeyword string

const (
	schemaReference             schemaKeyword = "$ref"
	schemaDynamicReference      schemaKeyword = "$dynamicRef"
	schemaRecursiveReference    schemaKeyword = "$recursiveRef"
	schemaIdentifier            schemaKeyword = "$id"
	schemaDialect               schemaKeyword = "$schema"
	schemaProperties            schemaKeyword = "properties"
	schemaPatternProperties     schemaKeyword = "patternProperties"
	schemaDefinitions           schemaKeyword = "$defs"
	schemaLegacyDefinitions     schemaKeyword = "definitions"
	schemaDependentSchemas      schemaKeyword = "dependentSchemas"
	schemaDependencies          schemaKeyword = "dependencies"
	schemaAllOf                 schemaKeyword = "allOf"
	schemaAnyOf                 schemaKeyword = "anyOf"
	schemaOneOf                 schemaKeyword = "oneOf"
	schemaPrefixItems           schemaKeyword = "prefixItems"
	schemaItems                 schemaKeyword = "items"
	schemaAdditionalItems       schemaKeyword = "additionalItems"
	schemaAdditionalProperties  schemaKeyword = "additionalProperties"
	schemaContains              schemaKeyword = "contains"
	schemaContentSchema         schemaKeyword = "contentSchema"
	schemaIf                    schemaKeyword = "if"
	schemaThen                  schemaKeyword = "then"
	schemaElse                  schemaKeyword = "else"
	schemaNot                   schemaKeyword = "not"
	schemaPropertyNames         schemaKeyword = "propertyNames"
	schemaUnevaluatedItems      schemaKeyword = "unevaluatedItems"
	schemaUnevaluatedProperties schemaKeyword = "unevaluatedProperties"
)

func (s schemaKeyword) relocates(value any) bool {
	switch s {
	case schemaReference, schemaDynamicReference, schemaRecursiveReference, schemaIdentifier, schemaDialect:
		return true
	case schemaProperties, schemaPatternProperties, schemaDefinitions, schemaLegacyDefinitions, schemaDependentSchemas, schemaDependencies:
		children, _ := value.(map[string]any)
		for _, child := range children {
			if relocatesSchema(child) {
				return true
			}
		}
	case schemaAllOf, schemaAnyOf, schemaOneOf, schemaPrefixItems, schemaItems:
		if children, ok := value.([]any); ok {
			for _, child := range children {
				if relocatesSchema(child) {
					return true
				}
			}
			return false
		}
		return relocatesSchema(value)
	case schemaAdditionalItems, schemaAdditionalProperties, schemaContains, schemaContentSchema, schemaIf, schemaThen, schemaElse, schemaNot, schemaPropertyNames, schemaUnevaluatedItems, schemaUnevaluatedProperties:
		return relocatesSchema(value)
	}
	return false
}

func relocatesSchema(value any) bool {
	object, _ := value.(map[string]any)
	for keyword, child := range object {
		if schemaKeyword(keyword).relocates(child) {
			return true
		}
	}
	return false
}
