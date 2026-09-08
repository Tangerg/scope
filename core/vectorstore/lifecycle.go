package vectorstore

// Closer releases the resources a store created for itself.
//
// It is a capability rather than a method every store carries, because most
// stores create nothing: they are handed a client, session, pool or collection
// and hold it for as long as the caller keeps it open. Closing a caller-owned
// resource is not cleanup but sabotage — one client normally serves several
// collections, so a store that closed it would take down every other store
// sharing it. A store in that position implements nothing here, and a caller
// detects the difference the way it detects every other capability:
//
//	if closer, ok := store.(vectorstore.Closer); ok {
//		defer closer.Close()
//	}
//
// Answering with a no-op Close instead is worse than answering with nothing. It
// tells a caller that this store has resources to release and that calling
// Close released them, and both are false; the caller then cannot tell the
// stores that need cleanup from the stores that do not, which is the only thing
// it asked.
type Closer interface {
	// Close releases what the store created. The store is unusable afterward,
	// and a second call is not a no-op: the underlying resource decides what a
	// repeat close means, and a gRPC connection reports an error for it.
	Close() error
}
