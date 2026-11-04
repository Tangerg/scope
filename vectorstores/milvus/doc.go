// Package milvus writes precalculated vectors and searches a provisioned
// Milvus collection. Set the schema description to SpaceID.StorageKey().
// The adapter verifies the actual collection, exact field policy and active
// vector index metric. The Host owns collection provisioning and the client.
//
// Query performs native ranking without enumerating the collection. A metadata
// predicate on opaque Core JSON needs an explicitly authorized QueryByScan and a
// ScanBudget. QueryByScan validates every visited record and rechecks returned
// membership; pagination and native ranking do not form a global snapshot.
//
// Upsert validates all columns before the RPC, never scans unrelated records,
// and does not confuse a lost or inconsistent mutation receipt with success.
package milvus
