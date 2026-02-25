package store

import (
	"database/sql"
	"fmt"
	"log"
)

// migration represents a database schema migration
type migration struct {
	version     int
	description string
	up          func(tx *sql.Tx) error
}

// migrations returns the ordered list of all schema migrations.
// Each migration runs inside a transaction.
//
// Rules for adding migrations:
//  1. Always append to the end — never reorder or modify existing entries.
//  2. For column/constraint changes, use the SQLite "recreate table" pattern:
//     CREATE new → INSERT SELECT → DROP old → ALTER RENAME.
//  3. Keep each migration idempotent where possible.
var migrations = []migration{
	{
		version:     1,
		description: "create projects table",
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				CREATE TABLE IF NOT EXISTS projects (
					id TEXT PRIMARY KEY,
					name TEXT NOT NULL,
					description TEXT NOT NULL DEFAULT '',
					odoo_version TEXT NOT NULL,
					postgres_version TEXT NOT NULL,
					port INTEGER NOT NULL,
					status TEXT NOT NULL DEFAULT 'stopped',
					created_at DATETIME NOT NULL,
					updated_at DATETIME NOT NULL
				)
			`)
			return err
		},
	},
	{
		version:     2,
		description: "add UNIQUE constraints to name and port",
		up: func(tx *sql.Tx) error {
			// SQLite cannot add constraints to existing tables,
			// so we recreate the table with the desired schema.
			if _, err := tx.Exec(`
				CREATE TABLE projects_new (
					id TEXT PRIMARY KEY,
					name TEXT NOT NULL UNIQUE,
					description TEXT NOT NULL DEFAULT '',
					odoo_version TEXT NOT NULL,
					postgres_version TEXT NOT NULL,
					port INTEGER NOT NULL UNIQUE,
					status TEXT NOT NULL DEFAULT 'stopped',
					created_at DATETIME NOT NULL,
					updated_at DATETIME NOT NULL
				)
			`); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO projects_new SELECT * FROM projects`); err != nil {
				return err
			}
			if _, err := tx.Exec(`DROP TABLE projects`); err != nil {
				return err
			}
			if _, err := tx.Exec(`ALTER TABLE projects_new RENAME TO projects`); err != nil {
				return err
			}
			return nil
		},
	},
	{
		version:     3,
		description: "add git_repo_url column and settings table",
		up: func(tx *sql.Tx) error {
			if _, err := tx.Exec(`ALTER TABLE projects ADD COLUMN git_repo_url TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
			if _, err := tx.Exec(`
				CREATE TABLE IF NOT EXISTS settings (
					key TEXT PRIMARY KEY,
					value TEXT NOT NULL DEFAULT ''
				)
			`); err != nil {
				return err
			}
			return nil
		},
	},
	{
		version:     4,
		description: "add git_repo_branch column",
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE projects ADD COLUMN git_repo_branch TEXT NOT NULL DEFAULT ''`)
			return err
		},
	},
	{
		version:     5,
		description: "add enterprise_enabled column",
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE projects ADD COLUMN enterprise_enabled INTEGER NOT NULL DEFAULT 0`)
			return err
		},
	},
	{
		version:     6,
		description: "add design_themes_enabled column",
		up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`ALTER TABLE projects ADD COLUMN design_themes_enabled INTEGER NOT NULL DEFAULT 0`)
			return err
		},
	},
}

// getSchemaVersion returns the current schema version using SQLite's built-in user_version pragma.
func getSchemaVersion(db *sql.DB) (int, error) {
	var version int
	err := db.QueryRow(`PRAGMA user_version`).Scan(&version)
	return version, err
}

// setSchemaVersion sets the schema version using SQLite's user_version pragma.
func setSchemaVersion(tx *sql.Tx, version int) error {
	// PRAGMA doesn't support parameter binding, so we use Sprintf.
	// The version is always an int from our migration list, so this is safe.
	_, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, version))
	return err
}

// runMigrations applies all pending migrations in order.
// Each migration runs in its own transaction for atomicity.
func runMigrations(db *sql.DB) error {
	current, err := getSchemaVersion(db)
	if err != nil {
		return fmt.Errorf("failed to get schema version: %w", err)
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}

		log.Printf("Running migration %d: %s", m.version, m.description)

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("migration %d: failed to begin transaction: %w", m.version, err)
		}

		if err := m.up(tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d (%s) failed: %w", m.version, m.description, err)
		}

		if err := setSchemaVersion(tx, m.version); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: failed to update schema version: %w", m.version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d: failed to commit: %w", m.version, err)
		}

		log.Printf("Migration %d complete", m.version)
	}

	return nil
}
