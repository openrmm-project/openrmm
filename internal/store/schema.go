package store

import _ "embed"

// Schema is the embedded, canonical database schema. It is idempotent and run on
// every server start. The copy at db/schema.sql is identical and kept for manual
// `psql` use; keep the two in sync.
//
//go:embed schema.sql
var Schema string
