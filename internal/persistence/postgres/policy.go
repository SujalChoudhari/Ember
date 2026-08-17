package postgres

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	"ember.local/ember/internal/ember"
	_ "github.com/lib/pq"
)

const RequiredMajor = 16
const SchemaComponent = "phase1"
const SchemaVersion = 1

type Config struct {
	MaxConnections       int
	ConnectTimeout       string
	StatementTimeout     string
	IdleTxTimeout        string
	RequiredMajorVersion int
}

func DefaultConfig() Config {
	return Config{MaxConnections: 10, ConnectTimeout: "5s", StatementTimeout: "15s", IdleTxTimeout: "30s", RequiredMajorVersion: RequiredMajor}
}

func (c Config) Validate() error {
	if c.MaxConnections != 10 || c.ConnectTimeout != "5s" || c.StatementTimeout != "15s" || c.IdleTxTimeout != "30s" || c.RequiredMajorVersion != RequiredMajor {
		return fmt.Errorf("incompatible PostgreSQL Phase 1 configuration")
	}
	return nil
}

//go:embed migrations/0001_phase1.sql
var migrationFS embed.FS

func Migrations() []string {
	migration, err := migrationFS.ReadFile("migrations/0001_phase1.sql")
	if err != nil {
		panic(err)
	}
	return []string{string(migration)}
}

func ApplyMigrations(ctx context.Context, db *sql.DB, migrations []string) error {
	if db == nil || len(migrations) == 0 {
		return fmt.Errorf("database and deterministic migrations are required")
	}
	if err := DefaultConfig().Validate(); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration transaction failed closed: %w", err)
	}
	for i, migration := range migrations {
		if migration == "" {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d is empty", i+1)
		}
		if _, err := tx.ExecContext(ctx, migration); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d failed closed: %w", i+1, err)
		}
	}
	var version, compatibleMin, compatibleMax int
	if err := tx.QueryRowContext(ctx, `SELECT version, compatible_min, compatible_max FROM schema_meta WHERE component=$1`, SchemaComponent).Scan(&version, &compatibleMin, &compatibleMax); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("schema compatibility metadata missing: %w", err)
	}
	if version != SchemaVersion || SchemaVersion < compatibleMin || SchemaVersion > compatibleMax || version < compatibleMin || version > compatibleMax {
		_ = tx.Rollback()
		return fmt.Errorf("incompatible Ember schema: version=%d compatible=[%d,%d] expected=%d", version, compatibleMin, compatibleMax, SchemaVersion)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migration commit failed closed: %w", err)
	}
	return nil
}

func Open(ctx context.Context, databaseURL string, files *ember.FileStore) (*Store, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("PostgreSQL database URL is required")
	}
	if files == nil {
		return nil, fmt.Errorf("filesystem provider is required")
	}
	if err := DefaultConfig().Validate(); err != nil {
		return nil, err
	}
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = db.Close()
		}
	}()
	cfg := DefaultConfig()
	db.SetMaxOpenConns(cfg.MaxConnections)
	db.SetMaxIdleConns(cfg.MaxConnections)
	db.SetConnMaxLifetime(30 * time.Minute)
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(connectCtx); err != nil {
		return nil, fmt.Errorf("PostgreSQL connection failed: %w", err)
	}
	if _, err := db.ExecContext(connectCtx, `SELECT set_config('statement_timeout', $1, false), set_config('idle_in_transaction_session_timeout', $2, false)`, cfg.StatementTimeout, cfg.IdleTxTimeout); err != nil {
		return nil, fmt.Errorf("PostgreSQL timeout policy failed: %w", err)
	}
	if err := ApplyMigrations(connectCtx, db, Migrations()); err != nil {
		return nil, err
	}
	store, err := newStore(db, files)
	if err != nil {
		return nil, err
	}
	closeOnError = false
	return store, nil
}
