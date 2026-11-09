package weaviate

import (
	"errors"
	"net"
	"testing"
)

func TestStoreRejectsMissingAndUninitializedClients(t *testing.T) {
	for _, sample := range []struct {
		name   string
		client *Client
		cause  error
	}{
		{name: "missing", cause: ErrMissingClient},
		{name: "zero", client: new(Client), cause: ErrUninitializedClient},
	} {
		t.Run(sample.name, func(t *testing.T) {
			config := StoreConfig{Client: sample.client, Collection: "Documents", Space: fixtureSpace}
			if err := config.Validate(); !errors.Is(err, sample.cause) {
				t.Fatalf("admission: %v", err)
			}
			if store, err := NewStore(t.Context(), config); store != nil || !errors.Is(err, sample.cause) {
				t.Fatalf("constructor: %v, %v", store, err)
			}
			if err := sample.client.Close(); !errors.Is(err, sample.cause) {
				t.Fatalf("close: %v", err)
			}
		})
	}
}

func TestClosedClientCannotAdmitAnotherStore(t *testing.T) {
	store := newNativeStore(t, &nativeFixture{})
	if err := store.client.Close(); err != nil {
		t.Fatal(err)
	}
	config := StoreConfig{Client: store.client, Collection: "Documents", Space: fixtureSpace}
	if err := config.Validate(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("admission: %v", err)
	}
	if another, err := NewStore(t.Context(), config); another != nil || !errors.Is(err, net.ErrClosed) {
		t.Fatalf("constructor: %v, %v", another, err)
	}
	if err := store.client.Close(); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
}
