package pinecone

import (
	"net"
	"testing"

	pineconesdk "github.com/pinecone-io/go-pinecone/v4/pinecone"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestNativeSDKConditionalDeleteWire(t *testing.T) {
	file, err := protoregistry.GlobalFiles.FindFileByPath("db_data_2025-04.proto")
	if err != nil {
		t.Fatal(err)
	}
	selection := metadataSelection([]string{`null`, `{}`, `{"large":9007199254740993,"nested":[null]}`})
	received := make(chan bool, 1)
	server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		request := dynamicpb.NewMessage(file.Messages().ByName("DeleteRequest"))
		if receiveErr := stream.RecvMsg(request); receiveErr != nil {
			return receiveErr
		}
		fields := request.Descriptor().Fields()
		method, _ := grpc.MethodFromServerStream(stream)
		valid := method == "/VectorService/Delete" && request.Get(fields.ByName("namespace")).String() == "fixture" && !request.Has(fields.ByName("ids")) && !request.Get(fields.ByName("delete_all")).Bool() && proto.Equal(selection, request.Get(fields.ByName("filter")).Message().Interface())
		received <- valid
		return stream.SendMsg(dynamicpb.NewMessage(file.Messages().ByName("DeleteResponse")))
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if serveErr := <-done; serveErr != nil {
			t.Error(serveErr)
		}
	})
	client, err := pineconesdk.NewClient(pineconesdk.NewClientParams{ApiKey: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := client.Index(pineconesdk.NewIndexConnParams{Host: "http://" + listener.Addr().String(), Namespace: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := connection.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	if err = connection.DeleteVectorsByFilter(t.Context(), selection); err != nil {
		t.Fatal(err)
	}
	if !<-received {
		t.Fatal("native SDK changed the conditional deletion filter or namespace")
	}
}
