# Run stores

A run store persists the evidence owned by [run](../agent/run/doc.go): the
original start receipt, acknowledged tree head, commit identity, and writer
fence. Reading this evidence never activates a writer.

[postgres](postgres/doc.go) is the PostgreSQL implementation. The Host provisions
its schema explicitly, supplies a connection pool, and may include a local
business write in the same transaction. External services are outside that
transaction. The database and process fault suite lives in
[the same driver module](postgres/postgres_test.go).
