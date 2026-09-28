package vectorstore

// Closer is implemented only by stores that create resources they must release.
// A store must not close a borrowed client, session, pool, or collection.
type Closer interface {
	// Close makes the store unusable. Repeated calls follow the underlying
	// resource's close semantics and are not guaranteed to be idempotent.
	Close() error
}
