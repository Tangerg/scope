package milvus

import (
	"github.com/milvus-io/milvus-proto/go-api/v2/commonpb"
	"github.com/milvus-io/milvus-proto/go-api/v2/milvuspb"
	"github.com/milvus-io/milvus-proto/go-api/v2/schemapb"
	"github.com/milvus-io/milvus/client/v2/entity"
	"github.com/milvus-io/milvus/client/v2/milvusclient"
)

// The SDK iterator interpolates VARCHAR cursors without escaping. Native
// iterator ordering with a typed template cursor preserves literal identities.
type sourcePage struct {
	collection string
	after      string
}

func (s sourcePage) Request() (*milvuspb.QueryRequest, error) {
	options := milvusclient.NewQueryOption(s.collection).WithLimit(sourcePageSize).
		WithOutputFields(fieldID, fieldContent, fieldMeta, fieldVector).WithConsistencyLevel(entity.ClStrong)
	if s.after != "" {
		options.WithFilter("id > {after}").WithTemplateParam("after", s.after)
	}
	request, err := options.Request()
	if err != nil {
		return nil, err
	}
	request.QueryParams = append(request.QueryParams, &commonpb.KeyValuePair{Key: milvusclient.IteratorKey, Value: "true"}, &commonpb.KeyValuePair{Key: "reduce_stop_for_best", Value: "true"})
	return request, nil
}

// Scope supplies selection facts; the native delete owns the condition and
// mutation under its strong-consistency timestamp. No ID-only retry is valid.
type nativeDeletion struct {
	collection string
	ids        []string
	metadata   *string
}

func (n nativeDeletion) Request() *milvuspb.DeleteRequest {
	request := milvusclient.NewDeleteOption(n.collection).WithExpr("id in {ids}").Request()
	request.ConsistencyLevel = commonpb.ConsistencyLevel_Strong
	request.ExprTemplateValues = map[string]*schemapb.TemplateValue{
		"ids": {Val: &schemapb.TemplateValue_ArrayVal{ArrayVal: &schemapb.TemplateArrayValue{Data: &schemapb.TemplateArrayValue_StringData{StringData: &schemapb.StringArray{Data: n.ids}}}}},
	}
	if n.metadata != nil {
		request.Expr += " and metadata == {metadata}"
		request.ExprTemplateValues["metadata"] = &schemapb.TemplateValue{Val: &schemapb.TemplateValue_StringVal{StringVal: *n.metadata}}
	}
	return request
}
