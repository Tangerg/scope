package bedrockkb

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentruntime/types"
)

func TestImplicitFilterRejectsInvalidMetadataBeforeIO(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*types.ImplicitFilterConfiguration)
	}{
		{"no attributes", func(c *types.ImplicitFilterConfiguration) { c.MetadataAttributes = nil }},
		{"too many attributes", func(c *types.ImplicitFilterConfiguration) {
			c.MetadataAttributes = make([]types.MetadataAttributeSchema, 26)
			for index := range c.MetadataAttributes {
				c.MetadataAttributes[index] = types.MetadataAttributeSchema{Key: aws.String(fmt.Sprintf("key%d", index)), Type: types.AttributeTypeString, Description: aws.String("description")}
			}
		}},
		{"nil key", func(c *types.ImplicitFilterConfiguration) { c.MetadataAttributes[0].Key = nil }},
		{"empty key", func(c *types.ImplicitFilterConfiguration) { c.MetadataAttributes[0].Key = aws.String("") }},
		{"invalid UTF8 key", func(c *types.ImplicitFilterConfiguration) { c.MetadataAttributes[0].Key = aws.String("\xff") }},
		{"long key", func(c *types.ImplicitFilterConfiguration) {
			c.MetadataAttributes[0].Key = aws.String(strings.Repeat("界", 257))
		}},
		{"nil description", func(c *types.ImplicitFilterConfiguration) { c.MetadataAttributes[0].Description = nil }},
		{"empty description", func(c *types.ImplicitFilterConfiguration) { c.MetadataAttributes[0].Description = aws.String("") }},
		{"invalid UTF8 description", func(c *types.ImplicitFilterConfiguration) { c.MetadataAttributes[0].Description = aws.String("\xff") }},
		{"long description", func(c *types.ImplicitFilterConfiguration) {
			c.MetadataAttributes[0].Description = aws.String(strings.Repeat("界", 1025))
		}},
		{"missing type", func(c *types.ImplicitFilterConfiguration) { c.MetadataAttributes[0].Type = "" }},
		{"unknown type", func(c *types.ImplicitFilterConfiguration) { c.MetadataAttributes[0].Type = "OBJECT" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			config := fixture.config()
			config.ImplicitFilterConfiguration = &types.ImplicitFilterConfiguration{
				ModelArn: aws.String("arn:implicit"),
				MetadataAttributes: []types.MetadataAttributeSchema{{
					Key: aws.String("tenant"), Type: types.AttributeTypeString, Description: aws.String("tenant name"),
				}},
			}
			test.change(config.ImplicitFilterConfiguration)
			retriever, err := New(t.Context(), config)
			if err == nil || retriever != nil {
				t.Fatalf("invalid implicit metadata constructed a retriever: %v", err)
			}
			if len(fixture.bodies) != 0 {
				t.Fatal("invalid implicit metadata reached HTTP")
			}
		})
	}
}

func TestImplicitFilterPreservesNativeMetadataBoundaries(t *testing.T) {
	for _, count := range []int{1, 25} {
		t.Run(fmt.Sprintf("%d attributes", count), func(t *testing.T) {
			fixture := newProtocolFixture(t, `{"retrievalResults":[]}`)
			config := fixture.config()
			attributeTypes := []types.AttributeType{types.AttributeTypeString, types.AttributeTypeNumber, types.AttributeTypeBoolean, types.AttributeTypeStringList}
			attributes := make([]types.MetadataAttributeSchema, count)
			for index := range attributes {
				attributes[index] = types.MetadataAttributeSchema{
					Key:         aws.String(fmt.Sprintf("key%d", index)),
					Type:        attributeTypes[index%len(attributeTypes)],
					Description: aws.String(" "),
				}
			}
			attributes[0].Key = aws.String(strings.Repeat("界", 256))
			attributes[0].Description = aws.String(strings.Repeat("界", 1024))
			config.ImplicitFilterConfiguration = &types.ImplicitFilterConfiguration{ModelArn: aws.String("arn:implicit"), MetadataAttributes: attributes}
			retriever, err := New(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = retriever.Retrieve(t.Context(), testQuery(t)); err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Configuration struct {
					Vector struct {
						Implicit struct {
							Attributes []struct {
								Key         string              `json:"key"`
								Type        types.AttributeType `json:"type"`
								Description string              `json:"description"`
							} `json:"metadataAttributes"`
						} `json:"implicitFilterConfiguration"`
					} `json:"vectorSearchConfiguration"`
				} `json:"retrievalConfiguration"`
			}
			encoded, err := fixture.bodies[0].MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if err = jsonv2.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			projected := wire.Configuration.Vector.Implicit.Attributes
			if len(projected) != len(attributes) {
				t.Fatalf("projected %d metadata attributes, want %d", len(projected), len(attributes))
			}
			for index, attribute := range projected {
				original := attributes[index]
				if attribute.Key != *original.Key || attribute.Type != original.Type || attribute.Description != *original.Description {
					t.Fatalf("implicit metadata changed at index %d: %+v", index, attribute)
				}
			}
		})
	}
}

func TestImplicitFilterRejectsInvalidModelReferenceBeforeIO(t *testing.T) {
	for _, test := range []struct {
		name  string
		model *string
	}{
		{"nil", nil},
		{"empty", aws.String("")},
		{"whitespace", aws.String(" ")},
		{"invalid UTF8", aws.String("\xff")},
		{"uppercase identifier", aws.String("Amazon.nova-pro-v1:0")},
		{"invalid account", aws.String("arn:aws:bedrock:us-east-1:123:foundation-model/amazon.nova-pro-v1:0")},
		{"unsupported service", aws.String("arn:aws:ec2:us-east-1::foundation-model/amazon.nova-pro-v1:0")},
		{"invalid resource path", aws.String("model/first/second")},
		{"trailing punctuation", aws.String("amazon.nova-pro-v1:0?")},
		{"trailing newline", aws.String("amazon.nova-pro-v1:0\n")},
		{"oversized reference", aws.String(implicitModelReferenceAtLength(2049))},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProtocolFixture(t)
			config := fixture.config()
			config.ImplicitFilterConfiguration = &types.ImplicitFilterConfiguration{
				ModelArn: test.model,
				MetadataAttributes: []types.MetadataAttributeSchema{{
					Key: aws.String("tenant"), Type: types.AttributeTypeString, Description: aws.String("tenant name"),
				}},
			}
			retriever, err := New(t.Context(), config)
			if err == nil || retriever != nil {
				t.Fatalf("invalid implicit model reference constructed a retriever: %v", err)
			}
			if len(fixture.bodies) != 0 {
				t.Fatal("invalid implicit model reference reached HTTP")
			}
		})
	}
}

func TestImplicitFilterPreservesModelReferences(t *testing.T) {
	for index, model := range []string{
		"a",
		"amazon.nova-pro-v1:0",
		"anthropic.claude-3-5-sonnet-20240620-v1:0",
		"us.amazon.nova-pro-v1:0",
		"arn:aws:bedrock:us-east-1::foundation-model/amazon.nova-pro-v1:0",
		"arn:aws:bedrock:us-east-1:123456789012:inference-profile/us.amazon.nova-pro-v1:0",
		"arn:aws-us-gov:bedrock:us-gov-west-1::foundation-model/amazon.nova-pro-v1:0",
		"arn:aws-cn:bedrock:cn-north-1::foundation-model/amazon.nova-pro-v1:0",
		"arn:aws:sagemaker:us-east-1:123456789012:endpoint/filter-model",
		implicitModelReferenceAtLength(2048),
	} {
		t.Run(fmt.Sprintf("model reference %d", index), func(t *testing.T) {
			fixture := newProtocolFixture(t, `{"retrievalResults":[]}`)
			config := fixture.config()
			config.ImplicitFilterConfiguration = &types.ImplicitFilterConfiguration{
				ModelArn: aws.String(model),
				MetadataAttributes: []types.MetadataAttributeSchema{{
					Key: aws.String("tenant"), Type: types.AttributeTypeString, Description: aws.String("tenant name"),
				}},
			}
			retriever, err := New(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = retriever.Retrieve(t.Context(), testQuery(t)); err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Configuration struct {
					Vector struct {
						Implicit struct {
							Model string `json:"modelArn"`
						} `json:"implicitFilterConfiguration"`
					} `json:"vectorSearchConfiguration"`
				} `json:"retrievalConfiguration"`
			}
			encoded, err := fixture.bodies[0].MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			if err = jsonv2.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.Configuration.Vector.Implicit.Model != model {
				t.Fatalf("implicit model reference changed: %q", wire.Configuration.Vector.Implicit.Model)
			}
		})
	}
}

// The native partition pattern is unbounded, so length needs an independent boundary case.
func implicitModelReferenceAtLength(length int) string {
	const prefix = "arn:aws-"
	const suffix = ":bedrock:us-east-1::foundation-model/amazon.nova-pro-v1:0"
	return prefix + strings.Repeat("x", length-len(prefix)-len(suffix)) + suffix
}
