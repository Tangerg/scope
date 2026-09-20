package agent

// Synthetic recovery tests install their fixture as an authoritative stored cut.
// This models a separate restored storage image, never a rewind of a live store.
// CAS, writer fencing, and historical deduplication are exercised by the public
// commit conformance suite against a store populated through real commits.
func newSnapshotTestCommitter(snapshot TreeSnapshot) *MemoryTreeCommitter {
	store := NewMemoryTreeCommitter()
	store.heads[snapshot.RootID()] = snapshot
	return store
}
