package resourcedb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/bgdnvk/clanker/internal/secfile"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS resources (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id          TEXT NOT NULL,
    command_index   INTEGER NOT NULL,
    provider        TEXT NOT NULL DEFAULT 'aws',
    service         TEXT NOT NULL,
    operation       TEXT NOT NULL,
    resource_type   TEXT NOT NULL,
    resource_id     TEXT,
    resource_arn    TEXT,
    resource_name   TEXT,
    region          TEXT,
    profile         TEXT,
    account_id      TEXT,
    parent_run_id   TEXT,
    metadata        TEXT,
    tags            TEXT,
    created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,

    UNIQUE(run_id, command_index)
);

CREATE INDEX IF NOT EXISTS idx_resources_run_id ON resources(run_id);
CREATE INDEX IF NOT EXISTS idx_resources_resource_type ON resources(resource_type);
CREATE INDEX IF NOT EXISTS idx_resources_resource_id ON resources(resource_id);
CREATE INDEX IF NOT EXISTS idx_resources_created_at ON resources(created_at);
CREATE INDEX IF NOT EXISTS idx_resources_parent_run_id ON resources(parent_run_id);
`

// DefaultDBPath returns the default database path (~/.clanker/resources.db)
func DefaultDBPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ".clanker/resources.db"
	}
	return filepath.Join(homeDir, ".clanker", "resources.db")
}

// openDB opens or creates the SQLite database
func openDB(dbPath string) (*sql.DB, error) {
	if dbPath == "" {
		dbPath = DefaultDBPath()
	}

	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := secfile.EnsurePrivateDir(dir); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set pragmas for better performance
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=10000",
		"PRAGMA temp_store=MEMORY",
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to set pragma: %w", err)
		}
	}
	if err := hardenSQLiteFiles(dbPath); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

// migrate runs schema migrations
func migrate(db *sql.DB) error {
	_, err := db.Exec(schema)
	if err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}
	return nil
}

func hardenSQLiteFiles(dbPath string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Chmod(path, secfile.PrivateFileMode); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to secure database file %s: %w", path, err)
		}
	}
	return nil
}
