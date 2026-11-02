package postgres

import (
	_ "embed"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

const maxIdentifierBytes = 63
const schemaMarker = "$schema"

//go:embed schema.sql
var schemaTemplate string

type relations struct{ heads, log string }

func namespace(name string) (string, error) {
	if name == "" || len(name) > maxIdentifierBytes || !utf8.ValidString(name) || strings.ContainsRune(name, 0) {
		return "", errors.New("postgres: invalid schema name")
	}
	return pgx.Identifier{name}.Sanitize(), nil
}

func newRelations(schema string) relations {
	return relations{heads: schema + ".heads", log: schema + ".log"}
}

// SchemaSQL returns strict DDL for an existing, Host-owned namespace. There is
// no IF NOT EXISTS: an incompatible or pre-existing store must not be blessed.
func SchemaSQL(name string) (string, error) {
	schema, err := namespace(name)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(schemaTemplate, schemaMarker, schema), nil
}
