package postgres

import (
	_ "embed"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/Tangerg/scope/agent/run"
)

const maxIdentifierBytes = 63

const (
	schemaMarker     = "$schema"
	checkpointMarker = "$checkpoint"
)

//go:embed schema.sql
var schemaTemplate string

func namespace(name string) (string, error) {
	if name == "" || len(name) > maxIdentifierBytes || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return "", errors.New("postgres: invalid schema name")
	}
	return pgx.Identifier{name}.Sanitize(), nil
}

func logRelation(schema string) string { return schema + ".log" }

// SchemaSQL returns strict DDL for an existing, Host-owned namespace. There is
// no IF NOT EXISTS: an incompatible or pre-existing store must not be blessed.
func SchemaSQL(name string) (string, error) {
	schema, err := namespace(name)
	if err != nil {
		return "", err
	}
	return strings.NewReplacer(schemaMarker, schema, checkpointMarker, checkpointKind).Replace(schemaTemplate), nil
}

// checkpointKind is the SQL literal of the checkpoint record kind. The partial
// index and the recovery query must spell it identically: a bound parameter
// would let a generic plan miss the index predicate and scan the whole log.
const checkpointKind = "'" + string(run.RecordKindCheckpoint) + "'"
