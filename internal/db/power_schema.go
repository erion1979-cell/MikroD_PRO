package db

// The Power/UPS module's tables (docs/inverter/PLAN.md). Port-added, migration
// 35. One constant for the fresh schema and the migration, the rule
// `monitorRunsDDL` and the others follow, so the two cannot describe different
// tables.
//
// ── power_units ────────────────────────────────────────────────────────────
//
// One row per inverter or UPS. The converter in front of it is just
// `host:port` plus the unit's Modbus slave id: what make of converter it is
// does not matter, provided it runs as a Modbus TCP gateway. `model` is the
// definition's ID, producer/model (internal/power/model). `router_id` is an
// optional link to the site router, for display only: nothing is purged or
// gated through it, so a deleted router leaves a dangling id that simply shows
// nothing.
//
// ── power_minutes AND power_samples ────────────────────────────────────────
//
// One history row per unit per minute (internal/power/bucket.go), split in two:
// the poll counts once per minute, and one row per measure, so a model that
// reports a measure the first one did not needs no new column.
//
// ── power_events ────────────────────────────────────────────────────────────
//
// One row per condition (internal/power/track.go): `ended_at` is NULL while it
// is still true. `initial` marks one that was already true when monitoring
// began, whose `began_at` is only when it was first seen.
//
// Deleting a unit deletes its history and events, by the foreign keys; the
// connection has `foreign_keys(1)` set (db.go).
const powerTablesDDL = `
CREATE TABLE IF NOT EXISTS power_units (
          id         TEXT    PRIMARY KEY,
          name       TEXT    NOT NULL,
          site_id    TEXT,
          model      TEXT    NOT NULL,
          host       TEXT    NOT NULL,
          port       INTEGER NOT NULL,
          slave_id   INTEGER NOT NULL,
          router_id  TEXT,
          battery_ah REAL,
          enabled    INTEGER NOT NULL DEFAULT 1,
          created_at INTEGER NOT NULL
        );

CREATE TABLE IF NOT EXISTS power_minutes (
          unit_id  TEXT    NOT NULL REFERENCES power_units(id) ON DELETE CASCADE,
          ts       INTEGER NOT NULL,
          polls    INTEGER NOT NULL,
          ok       INTEGER NOT NULL,
          reply_ms REAL    NOT NULL,
          PRIMARY KEY (unit_id, ts)
        );

CREATE TABLE IF NOT EXISTS power_samples (
          unit_id TEXT    NOT NULL REFERENCES power_units(id) ON DELETE CASCADE,
          ts      INTEGER NOT NULL,
          key     TEXT    NOT NULL,
          avg     REAL    NOT NULL,
          min     REAL    NOT NULL,
          max     REAL    NOT NULL,
          PRIMARY KEY (unit_id, ts, key)
        );

CREATE TABLE IF NOT EXISTS power_events (
          id       INTEGER PRIMARY KEY,
          unit_id  TEXT    NOT NULL REFERENCES power_units(id) ON DELETE CASCADE,
          kind     TEXT    NOT NULL,
          code     INTEGER NOT NULL DEFAULT 0,
          text     TEXT    NOT NULL,
          fault    INTEGER NOT NULL DEFAULT 0,
          initial  INTEGER NOT NULL DEFAULT 0,
          began_at INTEGER NOT NULL,
          ended_at INTEGER
        );
CREATE INDEX IF NOT EXISTS idx_power_events_unit ON power_events(unit_id, began_at);
`
