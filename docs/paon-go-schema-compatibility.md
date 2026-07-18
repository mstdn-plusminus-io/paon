# Paon Go schema compatibility

`internal/paon/migrate/schema.sql` is the reviewed, embedded PostgreSQL schema snapshot. Ruby and Rails are not required to initialize or validate a database.

| Starting state | Supported action |
|---|---|
| Empty PostgreSQL database | `paon-migrate` creates the complete schema, functions, sequences, indexes, foreign keys, materialized views, metadata, and production seed rows. |
| Compatible schema version `20230907150100` | `paon-migrate` validates the complete Paon schema and exits without changing data. |
| Any older/newer/partial schema | Refused. Upgrade or downgrade with a release that explicitly supports that starting version. |

Migration execution uses a PostgreSQL transaction-scoped advisory lock. Fresh schema application is atomic, concurrent invocations serialize, GORM AutoMigrate remains disabled, and the snowflake `timestamp_id` function receives a new random salt for each database.

When the database contract changes:

1. update and review `internal/paon/migrate/schema.sql`;
2. add an explicit versioned upgrade path before changing `CurrentSchemaVersion`;
3. update models and startup schema guards for tables, columns, defaults, indexes, keys, functions, sequences, and views;
4. run migration integration tests against an empty database and every supported starting version;
5. document rollback and cutover behavior.
