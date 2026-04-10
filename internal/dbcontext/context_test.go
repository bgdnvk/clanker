package dbcontext

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOpenConfigSQLiteUsesReadOnlyURI(t *testing.T) {
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "inspect.sqlite")

	writer, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open writer db: %v", err)
	}
	defer writer.Close()

	if _, err := writer.Exec(`create table widgets (id integer primary key, name text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := writer.Exec(`insert into widgets (name) values ('alpha')`); err != nil {
		t.Fatalf("insert row: %v", err)
	}

	driverName, dsn, err := openConfig(Connection{Driver: "sqlite", Path: dbPath, Name: "dev"})
	if err != nil {
		t.Fatalf("openConfig: %v", err)
	}
	if driverName != "sqlite" {
		t.Fatalf("expected sqlite driver, got %q", driverName)
	}
	if !strings.Contains(dsn, "mode=ro") {
		t.Fatalf("expected read-only sqlite dsn, got %q", dsn)
	}

	reader, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatalf("open reader db: %v", err)
	}
	defer reader.Close()

	if _, err := reader.ExecContext(context.Background(), `insert into widgets (name) values ('beta')`); err == nil {
		t.Fatal("expected sqlite read-only connection to reject writes")
	}

	var count int
	if err := reader.QueryRowContext(context.Background(), `select count(*) from widgets`).Scan(&count); err != nil {
		t.Fatalf("count widgets: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row, got %d", count)
	}
}

func TestPostgresDSNEnforcesReadOnlySession(t *testing.T) {
	dsn, err := postgresDSN(Connection{
		Driver:   "postgres",
		Name:     "dev",
		Host:     "localhost",
		Port:     5432,
		Database: "app",
		Username: "postgres",
	})
	if err != nil {
		t.Fatalf("postgresDSN: %v", err)
	}

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	if got := parsed.Query().Get("default_transaction_read_only"); got != "on" {
		t.Fatalf("expected default_transaction_read_only=on, got %q", got)
	}
	if got := parsed.Query().Get("sslmode"); got == "" {
		t.Fatal("expected sslmode to be present")
	}
}

func TestSQLiteReadOnlyDSNNormalizesPath(t *testing.T) {
	readonly := sqliteReadOnlyDSN("./state.sqlite")
	if !strings.HasPrefix(readonly, "file:") {
		t.Fatalf("expected sqlite read-only uri, got %q", readonly)
	}
	if !strings.Contains(readonly, "mode=ro") {
		t.Fatalf("expected read-only mode in uri, got %q", readonly)
	}
}
