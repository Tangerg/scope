// Package cassandra is a history Store backed by Apache Cassandra
// via gocql.
//
// Schema (created by InitializeSchema=true):
//
//	CREATE TABLE <keyspace>.<table> (
//	    conversation_id TEXT,
//	    seq             TIMEUUID,
//	    message         TEXT,
//	    PRIMARY KEY ((conversation_id), seq)
//	) WITH CLUSTERING ORDER BY (seq ASC);
//
// `conversation_id` is the partition key and `seq` is a client-generated
// TIMEUUID clustering key. Each Write reserves a strictly increasing local
// sequence range and sends one unlogged batch to that partition. Concurrent
// calls and writes from distinct Store instances have no defined relative
// order.
//
// Write acknowledgment. The session must not use consistency ANY. At every
// other level a successful write reached at least one replica, but at ANY
// Cassandra may instead have the coordinator "store a hint" and replay it
// later — the call returns nil for a message no replica holds, which no read
// can see and which is gone if the hint expires. [NewStore] refuses that
// session with [ErrUnacknowledgedWrites]. ANY is also the zero value of
// gocql.Consistency, so a session whose consistency was never configured is
// refused for the same reason.
//
// Example:
//
//	cluster := gocql.NewCluster("127.0.0.1")
//	cluster.Keyspace = "scope"
//	sess, _ := cluster.CreateSession()
//	defer sess.Close()
//
//	store, _ := cassandra.NewStore(ctx, cassandra.StoreConfig{
//	    Session:          sess,
//	    Keyspace:         "scope",
//	    InitializeSchema: true,
//	})
package cassandra
