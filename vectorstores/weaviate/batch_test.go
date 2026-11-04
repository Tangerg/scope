package weaviate

import (
	"errors"
	"slices"
	"testing"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Tangerg/scope/core/document"
	"github.com/Tangerg/scope/core/vectorstore"
)

func TestNativeBatchOutcomesUseRPCAndPositionalErrors(t *testing.T) {
	ids := []string{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000003"}
	docs := make([]*document.Document, len(ids))
	for i, id := range ids {
		docs[i] = &document.Document{ID: id, Text: "text"}
	}
	for _, fault := range []string{"all acknowledged", "middle refused", "lost batch reply", "unknown position", "duplicate position"} {
		t.Run(fault, func(t *testing.T) {
			f := &nativeFixture{batchReply: func(_ *pb.BatchObjectsRequest) (*pb.BatchObjectsReply, error) {
				switch fault {
				case "middle refused":
					return &pb.BatchObjectsReply{Errors: []*pb.BatchObjectsReply_BatchError{{Index: 1, Error: "invalid item"}}}, nil
				case "lost batch reply":
					return nil, status.Error(codes.Unavailable, "lost ack")
				case "duplicate position":
					return &pb.BatchObjectsReply{Errors: []*pb.BatchObjectsReply_BatchError{{Index: 1, Error: "failure"}, {Index: 1, Error: "conflicting entry"}}}, nil
				case "unknown position":
					return &pb.BatchObjectsReply{Errors: []*pb.BatchObjectsReply_BatchError{{Index: 7, Error: "unaddressable failure"}}}, nil
				}
				return &pb.BatchObjectsReply{}, nil
			}}
			s := newNativeStore(t, f)
			err := s.Upsert(t.Context(), fixtureBatch(docs))
			if fault == "all acknowledged" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var w *vectorstore.WriteError
			if !errors.As(err, &w) {
				t.Fatalf("lost typed evidence: %v", err)
			}
			if fault == "middle refused" {
				if !slices.Equal(w.Outcome().Acknowledged, []string{ids[0], ids[2]}) || !slices.Equal(w.Outcome().Rejected, ids[1:2]) || len(w.Outcome().Unconfirmed) != 0 {
					t.Fatalf("mixed=%#v", w)
				}
			} else if !slices.Equal(w.Outcome().Unconfirmed, ids) || len(w.Outcome().Acknowledged) != 0 {
				t.Fatalf("unconfirmed batch=%#v", w)
			}
		})
	}
}
