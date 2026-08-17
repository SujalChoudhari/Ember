package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

const RequiredMajor = 16
const SchemaComponent = "phase1"
const SchemaVersion = 1

type Config struct {
	MaxConnections      int
	ConnectTimeout      string
	StatementTimeout    string
	IdleTxTimeout       string
	RequiredMajorVersion int
}

func DefaultConfig() Config { return Config{MaxConnections: 10, ConnectTimeout: "5s", StatementTimeout: "15s", IdleTxTimeout: "30s", RequiredMajorVersion: RequiredMajor} }
func (c Config) Validate() error { if c.MaxConnections != 10 || c.ConnectTimeout != "5s" || c.StatementTimeout != "15s" || c.IdleTxTimeout != "30s" || c.RequiredMajorVersion != RequiredMajor { return fmt.Errorf("incompatible PostgreSQL Phase 1 configuration") }; return nil }

func ApplyMigrations(ctx context.Context, db *sql.DB, migrations []string) error {
	if db == nil || len(migrations) == 0 { return fmt.Errorf("database and deterministic migrations are required") }
	if err := DefaultConfig().Validate(); err != nil { return err }
	if _, err := db.ExecContext(ctx, migrations[0]); err != nil { return fmt.Errorf("migration failed closed: %w", err) }
	return nil
}
