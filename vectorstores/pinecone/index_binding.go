package pinecone

import (
	"context"
	"fmt"

	pineconesdk "github.com/pinecone-io/go-pinecone/v7/pinecone"

	"github.com/Tangerg/scope/core/vectorstore"
)

type indexPolicy interface {
	DescribeIndex(context.Context, string) (*pineconesdk.Index, error)
}

// The opening binding is immutable: live policy reads may reject drift but
// must never silently redirect the connection or reinterpret stored vectors.
type indexBinding struct {
	control indexPolicy
	name    string
	host    string
	space   vectorstore.SpaceID
	schema  nativeSchema
}

func readIndexBinding(ctx context.Context, control indexPolicy, name string, space vectorstore.SpaceID) (indexBinding, error) {
	native, err := control.DescribeIndex(ctx, name)
	if err != nil {
		return indexBinding{}, fmt.Errorf("pinecone: describe native index: %w", err)
	}
	if native == nil || native.Tags == nil || (*native.Tags)[SpaceTag] != string(space) {
		return indexBinding{}, vectorstore.ErrIncompatibleSpace
	}
	var schema nativeSchema
	if err := schema.read(native, name); err != nil {
		return indexBinding{}, err
	}
	return indexBinding{control: control, name: name, host: native.Host, space: space, schema: schema}, nil
}

func (i indexBinding) verify(ctx context.Context) error {
	current, err := readIndexBinding(ctx, i.control, i.name, i.space)
	if err != nil {
		return err
	}
	if current.host != i.host || current.schema != i.schema {
		return ErrIncompatibleIndex
	}
	return nil
}

func (i indexBinding) admitQuery(space vectorstore.SpaceID, options vectorstore.SearchOptions) (*pineconesdk.MetadataFilter, error) {
	if err := space.Require(i.space); err != nil {
		return nil, err
	}
	if err := options.RequireMode(vectorstore.SearchModeSemantic); err != nil {
		return nil, err
	}
	if options.ResultLimit() > MaxTopK {
		return nil, fmt.Errorf("%w: TopK exceeds native limit %d", vectorstore.ErrInvalidOptions, MaxTopK)
	}
	return nativeFilter(options.Filter)
}
