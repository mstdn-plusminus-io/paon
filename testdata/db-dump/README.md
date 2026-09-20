# Private staging dump

Place `mastodon_stg.dump` here to run `task test:migration-parity`.
The binary dump is intentionally ignored by Git. Its reviewed SHA-256 and the
four pinned upstream releases are recorded in `../migration-parity/versions.json`.

See [the migration parity guide](../../docs/migration-parity.md) for the full
restore, comparison, CI fixture, and verification boundaries.
