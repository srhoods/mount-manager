// Package db holds the PostgreSQL schema (idempotent; applied at server start).
package db

import _ "embed"

//go:embed schema.sql
var Schema string
