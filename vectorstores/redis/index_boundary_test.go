package redis

import (
	"context"
	"errors"
	"testing"

	goredis "github.com/redis/go-redis/v9"
)

type indexBoundaryClient struct {
	goredis.UniversalClient
	reply any
}

func (i *indexBoundaryClient) Do(ctx context.Context, _ ...any) *goredis.Cmd {
	command := goredis.NewCmd(ctx)
	command.SetVal(i.reply)
	return command
}

func TestExistingIndexNamespaceAndRepresentation(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"other prefix": func(v map[string]any) { v["index_definition"] = []any{"key_type", "HASH", "prefixes", []any{"other:"}} },
		"all keys":     func(v map[string]any) { v["index_definition"] = []any{"key_type", "HASH", "prefixes", []any{""}} },
		"multiple prefixes": func(v map[string]any) {
			v["index_definition"] = []any{"key_type", "HASH", "prefixes", []any{DefaultKeyPrefix, "other:"}}
		},
		"JSON": func(v map[string]any) {
			v["index_definition"] = []any{"key_type", "JSON", "prefixes", []any{DefaultKeyPrefix}}
		},
		"definition filter": func(v map[string]any) {
			v["index_definition"] = []any{"key_type", "HASH", "prefixes", []any{DefaultKeyPrefix}, "filter", "@tenant == 'x'"}
		},
		"options filter": func(v map[string]any) { v["index_options"] = []any{"FILTER", "@tenant == 'x'"} },
		"float64": func(v map[string]any) {
			a := vectorAttribute("COSINE", 2)
			a.DataType = "FLOAT64"
			v["attributes"] = indexReply(DefaultKeyPrefix, []goredis.FTAttribute{a})["attributes"]
		},
		"aliased vector": func(v map[string]any) {
			a := vectorAttribute("COSINE", 2)
			a.Identifier = "other"
			v["attributes"] = indexReply(DefaultKeyPrefix, []goredis.FTAttribute{a})["attributes"]
		},
	} {
		t.Run(name, func(t *testing.T) {
			reply := indexReply(DefaultKeyPrefix, []goredis.FTAttribute{vectorAttribute("COSINE", 2)})
			change(reply)
			store := &Store{client: &indexBoundaryClient{reply: reply}, keyPrefix: DefaultKeyPrefix, embeddingField: DefaultEmbeddingField, distanceMetric: DistanceCosine, dimensions: 2}
			if err := store.checkExistingIndex(t.Context()); !errors.Is(err, ErrIncompatibleIndex) {
				t.Fatalf("error %v", err)
			}
		})
	}
}

func TestHNSWConstructionSettingReachesSDKCommand(t *testing.T) {
	var args []any
	client := goredis.NewClient(&goredis.Options{Addr: "unused"})
	client.AddHook(&createCommandHook{args: &args})
	t.Cleanup(func() { _ = client.Close() })
	store := &Store{indexAlgorithm: AlgorithmHNSW, dimensions: 2, distanceMetric: DistanceCosine, hnswM: 16, hnswEFConstruct: 777, hnswEFRuntime: 10}
	client.FTCreate(t.Context(), "idx", &goredis.FTCreateOptions{OnHash: true}, &goredis.FieldSchema{FieldName: "embedding", FieldType: goredis.SearchFieldTypeVector, VectorArgs: store.vectorArgs()})
	for index, value := range args {
		if value == "EF_CONSTRUCTION" && index+1 < len(args) && args[index+1] == 777 {
			return
		}
	}
	t.Fatalf("FT.CREATE args %v", args)
}

type createCommandHook struct{ args *[]any }

func (c *createCommandHook) DialHook(next goredis.DialHook) goredis.DialHook { return next }
func (c *createCommandHook) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(_ context.Context, command goredis.Cmder) error { *c.args = command.Args(); return nil }
}
func (c *createCommandHook) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return next
}
