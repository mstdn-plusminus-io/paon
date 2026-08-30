package schemacatalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var schemaNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

func CheckGolden(ctx context.Context, database *sql.DB, schema string, goldenJSON []byte) error {
	golden, err := ParseGolden(goldenJSON)
	if err != nil {
		return err
	}
	catalog, err := Capture(ctx, database, schema)
	if err != nil {
		return err
	}
	difference, err := DiffGolden(golden, catalog)
	if err != nil {
		return err
	}
	if difference != "" {
		return fmt.Errorf("schema catalog does not match %s (%s at %s): %s", golden.Source.Name, golden.Source.Tag, golden.Source.Commit, difference)
	}
	return nil
}

func Capture(ctx context.Context, database *sql.DB, schema string) (Catalog, error) {
	if database == nil {
		return Catalog{}, errors.New("capture schema catalog: database is nil")
	}
	if !schemaNamePattern.MatchString(schema) {
		return Catalog{}, fmt.Errorf("capture schema catalog: invalid schema name %q", schema)
	}
	tx, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Catalog{}, fmt.Errorf("capture schema catalog: begin read-only transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('search_path', $1 || ',pg_catalog', true)`, schema); err != nil {
		return Catalog{}, fmt.Errorf("capture schema catalog: set search path: %w", err)
	}

	var versionNumber int
	if err := tx.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::integer`).Scan(&versionNumber); err != nil {
		return Catalog{}, fmt.Errorf("capture schema catalog: read PostgreSQL version: %w", err)
	}
	postgresqlMajor := versionNumber / 10000
	if postgresqlMajor < 14 {
		return Catalog{}, fmt.Errorf("capture schema catalog: PostgreSQL major %d is unsupported by strict catalog format %d; use PostgreSQL 14 or newer", postgresqlMajor, FormatVersion)
	}

	indexCatalogQuery := indexesQuery
	if postgresqlMajor < 15 {
		indexCatalogQuery = strings.Replace(indexCatalogQuery, "index_data.indnullsnotdistinct AS nulls_not_distinct", "false AS nulls_not_distinct", 1)
	}
	catalog := Catalog{Schema: schema, PostgreSQLMajor: postgresqlMajor}
	queries := []struct {
		name  string
		query string
		dest  any
	}{
		{"extensions", extensionsQuery, &catalog.Extensions},
		{"relations", relationsQuery, &catalog.Relations},
		{"columns", columnsQuery, &catalog.Columns},
		{"dropped columns", droppedColumnsQuery, &catalog.DroppedColumns},
		{"indexes", indexCatalogQuery, &catalog.Indexes},
		{"constraints", constraintsQuery, &catalog.Constraints},
		{"views", viewsQuery, &catalog.Views},
		{"functions", functionsQuery, &catalog.Functions},
		{"sequences", sequencesQuery, &catalog.Sequences},
		{"triggers", triggersQuery, &catalog.Triggers},
		{"rules", rulesQuery, &catalog.Rules},
		{"policies", policiesQuery, &catalog.Policies},
		{"types", typesQuery, &catalog.Types},
	}
	for _, item := range queries {
		if err := queryJSON(ctx, tx, item.query, item.dest, schema); err != nil {
			return Catalog{}, fmt.Errorf("capture schema catalog %s: %w", item.name, err)
		}
	}

	quotedSchema := `"` + schema + `"`
	if err := queryJSON(ctx, tx, `SELECT COALESCE(jsonb_agg(version ORDER BY version), '[]'::jsonb) FROM `+quotedSchema+`.schema_migrations`, &catalog.MigrationVersions); err != nil {
		return Catalog{}, fmt.Errorf("capture schema catalog migration versions: %w", err)
	}
	if err := queryJSON(ctx, tx, `
		SELECT COALESCE(jsonb_agg(to_jsonb(item) ORDER BY item.key), '[]'::jsonb)
		FROM (
			SELECT key, value
			FROM `+quotedSchema+`.ar_internal_metadata
		) AS item`, &catalog.ActiveRecordMetadata); err != nil {
		return Catalog{}, fmt.Errorf("capture schema catalog Active Record metadata: %w", err)
	}
	if err := normalizeTimestampIDFunction(catalog.Functions); err != nil {
		return Catalog{}, fmt.Errorf("capture schema catalog: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Catalog{}, fmt.Errorf("capture schema catalog: commit read-only transaction: %w", err)
	}
	return catalog, nil
}

func queryJSON(ctx context.Context, tx *sql.Tx, query string, destination any, arguments ...any) error {
	var raw []byte
	if err := tx.QueryRowContext(ctx, query, arguments...).Scan(&raw); err != nil {
		return err
	}
	if len(raw) == 0 {
		raw = []byte("[]")
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return fmt.Errorf("decode catalog query result: %w", err)
	}
	return nil
}

const extensionsQuery = `
SELECT COALESCE(jsonb_agg(to_jsonb(item) ORDER BY item.name), '[]'::jsonb)
FROM (
	SELECT extension_data.extname AS name,
	       namespace_data.nspname AS schema,
	       extension_data.extversion AS version,
	       extension_data.extrelocatable AS relocatable
	FROM pg_extension extension_data
	JOIN pg_namespace namespace_data ON namespace_data.oid = extension_data.extnamespace
	WHERE $1 <> ''
) AS item`

const relationsQuery = `
SELECT COALESCE(jsonb_agg(to_jsonb(item) ORDER BY item.name), '[]'::jsonb)
FROM (
	SELECT relation_data.relname AS name,
	       relation_data.relkind::text AS kind,
	       relation_data.relpersistence::text AS persistence,
	       COALESCE(access_method.amname, '') AS access_method,
	       relation_data.relreplident::text AS replica_identity,
	       relation_data.relrowsecurity AS row_security,
	       relation_data.relforcerowsecurity AS force_row_security,
	       relation_data.relispopulated AS populated,
	       COALESCE((SELECT jsonb_agg(option ORDER BY option) FROM unnest(relation_data.reloptions) AS option), '[]'::jsonb) AS options,
	       obj_description(relation_data.oid, 'pg_class') AS comment
	FROM pg_class relation_data
	JOIN pg_namespace namespace_data ON namespace_data.oid = relation_data.relnamespace
	LEFT JOIN pg_am access_method ON access_method.oid = relation_data.relam
	WHERE namespace_data.nspname = $1
	  AND relation_data.relkind IN ('r', 'p', 'v', 'm')
) AS item`

const columnsQuery = `
SELECT COALESCE(jsonb_agg(to_jsonb(item) ORDER BY item.relation, item.position), '[]'::jsonb)
FROM (
	SELECT relation_data.relname AS relation,
	       attribute_data.attnum::integer AS position,
	       attribute_data.attname AS name,
	       format_type(attribute_data.atttypid, attribute_data.atttypmod) AS type,
	       attribute_data.attnotnull AS not_null,
	       pg_get_expr(default_data.adbin, default_data.adrelid, false) AS default,
	       attribute_data.attidentity::text AS identity,
	       attribute_data.attgenerated::text AS generated,
	       CASE WHEN attribute_data.attcollation = 0 THEN '' ELSE collation_namespace.nspname || '.' || collation_data.collname END AS collation,
	       attribute_data.attstorage::text AS storage,
	       attribute_data.attcompression::text AS compression,
	       attribute_data.attstattarget AS statistics_target,
	       attribute_data.atthasmissing AS has_missing,
	       CASE WHEN attribute_data.atthasmissing THEN attribute_data.attmissingval::text END AS missing_value,
	       COALESCE((SELECT jsonb_agg(option ORDER BY option) FROM unnest(attribute_data.attoptions) AS option), '[]'::jsonb) AS options,
	       col_description(relation_data.oid, attribute_data.attnum) AS comment
	FROM pg_attribute attribute_data
	JOIN pg_class relation_data ON relation_data.oid = attribute_data.attrelid
	JOIN pg_namespace namespace_data ON namespace_data.oid = relation_data.relnamespace
	LEFT JOIN pg_attrdef default_data ON default_data.adrelid = attribute_data.attrelid AND default_data.adnum = attribute_data.attnum
	LEFT JOIN pg_collation collation_data ON collation_data.oid = attribute_data.attcollation
	LEFT JOIN pg_namespace collation_namespace ON collation_namespace.oid = collation_data.collnamespace
	WHERE namespace_data.nspname = $1
	  AND relation_data.relkind IN ('r', 'p', 'v', 'm')
	  AND attribute_data.attnum > 0
	  AND NOT attribute_data.attisdropped
) AS item`

const droppedColumnsQuery = `
SELECT COALESCE(jsonb_agg(to_jsonb(item) ORDER BY item.relation, item.position), '[]'::jsonb)
FROM (
	SELECT relation_data.relname AS relation,
	       attribute_data.attnum::integer AS position,
	       attribute_data.attname AS name
	FROM pg_attribute attribute_data
	JOIN pg_class relation_data ON relation_data.oid = attribute_data.attrelid
	JOIN pg_namespace namespace_data ON namespace_data.oid = relation_data.relnamespace
	WHERE namespace_data.nspname = $1
	  AND relation_data.relkind IN ('r', 'p')
	  AND attribute_data.attnum > 0
	  AND attribute_data.attisdropped
) AS item`

const indexesQuery = `
SELECT COALESCE(jsonb_agg(to_jsonb(item) ORDER BY item.table, item.name), '[]'::jsonb)
FROM (
	SELECT table_data.relname AS table,
	       index_relation.relname AS name,
	       access_method.amname AS access_method,
	       COALESCE(tablespace_data.spcname, '') AS tablespace,
	       index_data.indisunique AS unique,
	       index_data.indisprimary AS primary,
	       index_data.indisexclusion AS exclusion,
	       index_data.indimmediate AS immediate,
	       index_data.indisvalid AS valid,
	       index_data.indisready AS ready,
	       index_data.indisclustered AS clustered,
	       index_data.indisreplident AS replica_identity,
	       index_data.indnullsnotdistinct AS nulls_not_distinct,
	       index_data.indnkeyatts::integer AS key_count,
	       index_data.indnatts::integer AS attribute_count,
	       COALESCE((
	         SELECT jsonb_agg(pg_get_indexdef(index_data.indexrelid, position, false) ORDER BY position)
	         FROM generate_series(1, index_data.indnatts) AS position
	       ), '[]'::jsonb) AS keys,
	       pg_get_expr(index_data.indpred, index_data.indrelid, false) AS predicate,
	       pg_get_expr(index_data.indexprs, index_data.indrelid, false) AS expressions,
	       pg_get_indexdef(index_data.indexrelid, 0, false) AS definition,
	       COALESCE(constraint_data.conname, '') AS constraint,
	       COALESCE((SELECT jsonb_agg(option ORDER BY option) FROM unnest(index_relation.reloptions) AS option), '[]'::jsonb) AS options,
	       obj_description(index_relation.oid, 'pg_class') AS comment
	FROM pg_index index_data
	JOIN pg_class index_relation ON index_relation.oid = index_data.indexrelid
	JOIN pg_class table_data ON table_data.oid = index_data.indrelid
	JOIN pg_namespace namespace_data ON namespace_data.oid = table_data.relnamespace
	JOIN pg_am access_method ON access_method.oid = index_relation.relam
	LEFT JOIN pg_tablespace tablespace_data ON tablespace_data.oid = index_relation.reltablespace
	LEFT JOIN pg_constraint constraint_data ON constraint_data.conindid = index_data.indexrelid
	  AND constraint_data.conrelid = index_data.indrelid
	  AND constraint_data.contype IN ('p', 'u', 'x')
	WHERE namespace_data.nspname = $1
) AS item`

const constraintsQuery = `
SELECT COALESCE(jsonb_agg(to_jsonb(item) ORDER BY item.table, item.name), '[]'::jsonb)
FROM (
	SELECT table_data.relname AS table,
	       constraint_data.conname AS name,
	       constraint_data.contype::text AS type,
	       COALESCE((
	         SELECT jsonb_agg(attribute_data.attname ORDER BY key_data.ordinality)
	         FROM unnest(constraint_data.conkey) WITH ORDINALITY AS key_data(attribute_number, ordinality)
	         JOIN pg_attribute attribute_data ON attribute_data.attrelid = constraint_data.conrelid AND attribute_data.attnum = key_data.attribute_number
	       ), '[]'::jsonb) AS columns,
	       COALESCE(referenced_table.relname, '') AS referenced_table,
	       COALESCE((
	         SELECT jsonb_agg(attribute_data.attname ORDER BY key_data.ordinality)
	         FROM unnest(constraint_data.confkey) WITH ORDINALITY AS key_data(attribute_number, ordinality)
	         JOIN pg_attribute attribute_data ON attribute_data.attrelid = constraint_data.confrelid AND attribute_data.attnum = key_data.attribute_number
	       ), '[]'::jsonb) AS referenced_columns,
	       CASE WHEN constraint_data.contype = 'f' THEN constraint_data.confupdtype::text ELSE '' END AS update_action,
	       CASE WHEN constraint_data.contype = 'f' THEN constraint_data.confdeltype::text ELSE '' END AS delete_action,
	       CASE WHEN constraint_data.contype = 'f' THEN constraint_data.confmatchtype::text ELSE '' END AS match_type,
	       constraint_data.condeferrable AS deferrable,
	       constraint_data.condeferred AS deferred,
	       constraint_data.convalidated AS validated,
	       constraint_data.connoinherit AS no_inherit,
	       constraint_data.conislocal AS local,
	       constraint_data.coninhcount::integer AS inheritance_count,
	       COALESCE(backing_index.relname, '') AS backing_index,
	       COALESCE(parent_constraint.conname, '') AS parent_constraint,
	       pg_get_constraintdef(constraint_data.oid, false) AS definition,
	       obj_description(constraint_data.oid, 'pg_constraint') AS comment
	FROM pg_constraint constraint_data
	JOIN pg_class table_data ON table_data.oid = constraint_data.conrelid
	JOIN pg_namespace namespace_data ON namespace_data.oid = table_data.relnamespace
	LEFT JOIN pg_class referenced_table ON referenced_table.oid = constraint_data.confrelid
	LEFT JOIN pg_class backing_index ON backing_index.oid = constraint_data.conindid
	LEFT JOIN pg_constraint parent_constraint ON parent_constraint.oid = constraint_data.conparentid
	WHERE namespace_data.nspname = $1
) AS item`

const viewsQuery = `
SELECT COALESCE(jsonb_agg(to_jsonb(item) ORDER BY item.name), '[]'::jsonb)
FROM (
	SELECT relation_data.relname AS name,
	       relation_data.relkind::text AS kind,
	       relation_data.relispopulated AS populated,
	       pg_get_viewdef(relation_data.oid, false) AS definition,
	       obj_description(relation_data.oid, 'pg_class') AS comment
	FROM pg_class relation_data
	JOIN pg_namespace namespace_data ON namespace_data.oid = relation_data.relnamespace
	WHERE namespace_data.nspname = $1
	  AND relation_data.relkind IN ('v', 'm')
) AS item`

const functionsQuery = `
SELECT COALESCE(jsonb_agg(to_jsonb(item) ORDER BY item.name, item.identity_arguments), '[]'::jsonb)
FROM (
	SELECT procedure_data.proname AS name,
	       pg_get_function_identity_arguments(procedure_data.oid) AS identity_arguments,
	       pg_get_function_result(procedure_data.oid) AS result_type,
	       procedure_data.prokind::text AS kind,
	       language_data.lanname AS language,
	       procedure_data.provolatile::text AS volatility,
	       procedure_data.proparallel::text AS parallel,
	       procedure_data.proisstrict AS strict,
	       procedure_data.prosecdef AS security_definer,
	       procedure_data.proleakproof AS leakproof,
	       procedure_data.proretset AS returns_set,
	       procedure_data.procost::double precision AS cost,
	       procedure_data.prorows::double precision AS rows,
	       CASE WHEN procedure_data.prosupport = 0 THEN '' ELSE procedure_data.prosupport::regproc::text END AS support,
	       COALESCE(procedure_data.probin, '') AS binary,
	       COALESCE((SELECT jsonb_agg(configuration ORDER BY configuration) FROM unnest(procedure_data.proconfig) AS configuration), '[]'::jsonb) AS configuration,
	       pg_get_functiondef(procedure_data.oid) AS definition,
	       obj_description(procedure_data.oid, 'pg_proc') AS comment
	FROM pg_proc procedure_data
	JOIN pg_namespace namespace_data ON namespace_data.oid = procedure_data.pronamespace
	JOIN pg_language language_data ON language_data.oid = procedure_data.prolang
	WHERE namespace_data.nspname = $1
	  AND procedure_data.prokind <> 'a'
) AS item`

const sequencesQuery = `
SELECT COALESCE(jsonb_agg(to_jsonb(item) ORDER BY item.name), '[]'::jsonb)
FROM (
	SELECT sequence_relation.relname AS name,
	       sequence_relation.relpersistence::text AS persistence,
	       format_type(sequence_data.seqtypid, NULL) AS type,
	       sequence_data.seqstart AS start,
	       sequence_data.seqmin AS minimum,
	       sequence_data.seqmax AS maximum,
	       sequence_data.seqincrement AS increment,
	       sequence_data.seqcache AS cache,
	       sequence_data.seqcycle AS cycle,
	       COALESCE(owned_relation.relname, '') AS owned_by_table,
	       COALESCE(owned_attribute.attname, '') AS owned_by_column,
	       obj_description(sequence_relation.oid, 'pg_class') AS comment
	FROM pg_sequence sequence_data
	JOIN pg_class sequence_relation ON sequence_relation.oid = sequence_data.seqrelid
	JOIN pg_namespace namespace_data ON namespace_data.oid = sequence_relation.relnamespace
	LEFT JOIN LATERAL (
	  SELECT dependency_data.refobjid, dependency_data.refobjsubid
	  FROM pg_depend dependency_data
	  WHERE dependency_data.classid = 'pg_class'::regclass
	    AND dependency_data.objid = sequence_relation.oid
	    AND dependency_data.refclassid = 'pg_class'::regclass
	    AND dependency_data.deptype IN ('a', 'i')
	  ORDER BY dependency_data.deptype
	  LIMIT 1
	) AS ownership ON true
	LEFT JOIN pg_class owned_relation ON owned_relation.oid = ownership.refobjid
	LEFT JOIN pg_attribute owned_attribute ON owned_attribute.attrelid = ownership.refobjid AND owned_attribute.attnum = ownership.refobjsubid
	WHERE namespace_data.nspname = $1
) AS item`

const triggersQuery = `
SELECT COALESCE(jsonb_agg(to_jsonb(item) ORDER BY item.table, item.name), '[]'::jsonb)
FROM (
	SELECT table_data.relname AS table,
	       trigger_data.tgname AS name,
	       trigger_data.tgenabled::text AS enabled,
	       trigger_data.tgtype::integer AS type,
	       COALESCE(constraint_data.conname, '') AS constraint,
	       function_namespace.nspname || '.' || function_data.proname || '(' || pg_get_function_identity_arguments(function_data.oid) || ')' AS function,
	       pg_get_triggerdef(trigger_data.oid, false) AS definition,
	       obj_description(trigger_data.oid, 'pg_trigger') AS comment
	FROM pg_trigger trigger_data
	JOIN pg_class table_data ON table_data.oid = trigger_data.tgrelid
	JOIN pg_namespace namespace_data ON namespace_data.oid = table_data.relnamespace
	JOIN pg_proc function_data ON function_data.oid = trigger_data.tgfoid
	JOIN pg_namespace function_namespace ON function_namespace.oid = function_data.pronamespace
	LEFT JOIN pg_constraint constraint_data ON constraint_data.oid = trigger_data.tgconstraint
	WHERE namespace_data.nspname = $1
	  AND NOT trigger_data.tgisinternal
) AS item`

const rulesQuery = `
SELECT COALESCE(jsonb_agg(to_jsonb(item) ORDER BY item.table, item.name), '[]'::jsonb)
FROM (
	SELECT table_data.relname AS table,
	       rule_data.rulename AS name,
	       rule_data.ev_enabled::text AS enabled,
	       rule_data.ev_type::text AS event,
	       rule_data.is_instead AS instead,
	       pg_get_ruledef(rule_data.oid, false) AS definition,
	       obj_description(rule_data.oid, 'pg_rewrite') AS comment
	FROM pg_rewrite rule_data
	JOIN pg_class table_data ON table_data.oid = rule_data.ev_class
	JOIN pg_namespace namespace_data ON namespace_data.oid = table_data.relnamespace
	WHERE namespace_data.nspname = $1
	  AND rule_data.rulename <> '_RETURN'
) AS item`

const policiesQuery = `
SELECT COALESCE(jsonb_agg(to_jsonb(item) ORDER BY item.table, item.name), '[]'::jsonb)
FROM (
	SELECT table_data.relname AS table,
	       policy_data.polname AS name,
	       policy_data.polpermissive AS permissive,
	       policy_data.polcmd::text AS command,
	       COALESCE((
	         SELECT jsonb_agg(CASE WHEN role_oid = 0 THEN 'public' ELSE role_data.rolname END ORDER BY CASE WHEN role_oid = 0 THEN 'public' ELSE role_data.rolname END)
	         FROM unnest(policy_data.polroles) AS role_oid
	         LEFT JOIN pg_roles role_data ON role_data.oid = role_oid
	       ), '[]'::jsonb) AS roles,
	       pg_get_expr(policy_data.polqual, policy_data.polrelid, false) AS using,
	       pg_get_expr(policy_data.polwithcheck, policy_data.polrelid, false) AS with_check
	FROM pg_policy policy_data
	JOIN pg_class table_data ON table_data.oid = policy_data.polrelid
	JOIN pg_namespace namespace_data ON namespace_data.oid = table_data.relnamespace
	WHERE namespace_data.nspname = $1
) AS item`

const typesQuery = `
SELECT COALESCE(jsonb_agg(to_jsonb(item) ORDER BY item.name), '[]'::jsonb)
FROM (
	SELECT type_data.typname AS name,
	       type_data.typtype::text AS kind,
	       type_data.typcategory::text AS category,
	       CASE WHEN type_data.typbasetype = 0 THEN '' ELSE format_type(type_data.typbasetype, type_data.typtypmod) END AS base_type,
	       type_data.typnotnull AS not_null,
	       type_data.typdefault AS default,
	       CASE WHEN type_data.typcollation = 0 THEN '' ELSE collation_namespace.nspname || '.' || collation_data.collname END AS collation,
	       COALESCE((SELECT jsonb_agg(enum_data.enumlabel ORDER BY enum_data.enumsortorder) FROM pg_enum enum_data WHERE enum_data.enumtypid = type_data.oid), '[]'::jsonb) AS enum_labels,
	       COALESCE((SELECT jsonb_agg(pg_get_constraintdef(constraint_data.oid, false) ORDER BY constraint_data.conname) FROM pg_constraint constraint_data WHERE constraint_data.contypid = type_data.oid), '[]'::jsonb) AS domain_constraints,
	       COALESCE(format_type(range_data.rngsubtype, NULL), '') AS range_subtype,
	       obj_description(type_data.oid, 'pg_type') AS comment
	FROM pg_type type_data
	JOIN pg_namespace namespace_data ON namespace_data.oid = type_data.typnamespace
	LEFT JOIN pg_collation collation_data ON collation_data.oid = type_data.typcollation
	LEFT JOIN pg_namespace collation_namespace ON collation_namespace.oid = collation_data.collnamespace
	LEFT JOIN pg_range range_data ON range_data.rngtypid = type_data.oid OR range_data.rngmultitypid = type_data.oid
	WHERE namespace_data.nspname = $1
	  AND type_data.typrelid = 0
	  AND type_data.typtype IN ('d', 'e', 'r', 'm')
) AS item`
