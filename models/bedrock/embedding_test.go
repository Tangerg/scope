package bedrock

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClassifyEmbeddingModel(t *testing.T) {
	tests := []struct {
		model string
		want  embeddingFamily
	}{
		{"amazon.titan-embed-text-v1", embeddingFamilyTitanV1},
		{"amazon.titan-embed-text-v2:0", embeddingFamilyTitanV2},
		{"us.cohere.embed-english-v3", embeddingFamilyCohereV3},
		{"cohere.embed-multilingual-v3", embeddingFamilyCohereV3},
		{"arn:aws:bedrock:us-east-1:123:inference-profile/us.cohere.embed-v4:0", embeddingFamilyCohereV4},
	}
	for _, test := range tests {
		t.Run(test.model, func(t *testing.T) {
			got, err := classifyEmbeddingModel(test.model)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("classifyEmbeddingModel(%q) = %v, want %v", test.model, got, test.want)
			}
		})
	}
	if _, err := classifyEmbeddingModel("amazon.nova-unsupported"); err == nil {
		t.Fatal("classifyEmbeddingModel accepted an unsupported model")
	}
}

func TestValidateCohereEmbeddingOptions(t *testing.T) {
	for _, inputType := range []string{"search_document", "search_query", "classification", "clustering"} {
		if err := validateCohereInputType(inputType); err != nil {
			t.Fatalf("validateCohereInputType(%q): %v", inputType, err)
		}
	}
	for _, inputType := range []string{"", "image", "SEARCH_QUERY"} {
		if err := validateCohereInputType(inputType); err == nil {
			t.Fatalf("validateCohereInputType(%q) succeeded", inputType)
		}
	}

	if err := embeddingFamilyCohereV3.validateTruncate("START"); err != nil {
		t.Fatal(err)
	}
	if err := embeddingFamilyCohereV4.validateTruncate("LEFT"); err != nil {
		t.Fatal(err)
	}
	if err := embeddingFamilyCohereV3.validateTruncate("LEFT"); err == nil {
		t.Fatal("Cohere V3 accepted a V4 truncate value")
	}
	if err := embeddingFamilyCohereV4.validateTruncate("START"); err == nil {
		t.Fatal("Cohere V4 accepted a V3 truncate value")
	}
}

func TestValidateEmbeddingDimensions(t *testing.T) {
	allowed := int64(512)
	if err := validateDimensions("test", &allowed, 256, 512, 1024); err != nil {
		t.Fatal(err)
	}
	unsupported := int64(768)
	err := validateDimensions("test", &unsupported, 256, 512, 1024)
	if err == nil || !strings.Contains(err.Error(), "768") {
		t.Fatalf("validateDimensions error = %v", err)
	}
}

func TestDecodeCohereFloatEmbeddings(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"default float response", `[[0.1,0.2],[0.3,0.4]]`},
		{"embeddings by type", `{"float":[[0.1,0.2],[0.3,0.4]],"int8":[[1,2],[3,4]]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			vectors, err := decodeCohereFloatEmbeddings(json.RawMessage(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if len(vectors) != 2 || len(vectors[0]) != 2 {
				t.Fatalf("vectors = %#v", vectors)
			}
		})
	}
	if _, err := decodeCohereFloatEmbeddings(json.RawMessage(`{"int8":[[1,2]]}`)); err == nil {
		t.Fatal("decodeCohereFloatEmbeddings accepted a response without float vectors")
	}
}
