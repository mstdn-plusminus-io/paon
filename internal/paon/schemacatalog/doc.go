// Package schemacatalog captures and compares the deterministic PostgreSQL
// catalog contract of an upstream Mastodon database.
//
// Reference manifests must be generated from an upstream database with
// cmd/paon-schema-catalog. They must never be regenerated from Paon's own
// schema merely to accept a failing comparison. Routine tests need only the
// committed manifest and a PostgreSQL database containing the Paon migration
// result:
//
//	goldenJSON := []byte(...)
//	sqlDatabase, err := gormDatabase.DB()
//	if err != nil {
//		return err
//	}
//	if err := schemacatalog.CheckGolden(ctx, sqlDatabase, "public", goldenJSON); err != nil {
//		return err
//	}
//
// CheckGolden runs in a read-only repeatable-read transaction. It compares
// exact names and definitions, physical attribute order, dropped-column slots,
// constraints, indexes, routines, views, sequences, migration markers, and
// Active Record metadata. The random 32-hex timestamp_id salt is the sole
// normalized definition value. Instance OIDs, owners and ACLs, storage
// statistics, current sequence values, and internal referential-integrity
// trigger identifiers are intentionally outside the manifest.
package schemacatalog
