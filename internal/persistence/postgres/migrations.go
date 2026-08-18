package postgres

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
)

//go:embed migrations/0001_phase1.sql
var migrationFS embed.FS

func Migrations() []string {
	migrationSQL, err := migrationFS.ReadFile("migrations/0001_phase1.sql")
	if err != nil {
		panic(err)
	}
	return []string{string(migrationSQL)}
}

func ApplyMigrations(ctx context.Context, database *sql.DB, migrations []string) error {
	if database == nil || len(migrations) == 0 {
		return fmt.Errorf("database and deterministic migrations are required")
	}
	if err := DefaultConfig().Validate(); err != nil {
		return err
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migration transaction failed closed: %w", err)
	}
	for migrationIndex, migrationSQL := range migrations {
		if migrationSQL == "" {
			_ = transaction.Rollback()
			return fmt.Errorf("migration %d is empty", migrationIndex+1)
		}
		if _, err := transaction.ExecContext(ctx, migrationSQL); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("migration %d failed closed: %w", migrationIndex+1, err)
		}
	}
	var schemaVersion, compatibleMinimum, compatibleMaximum int
	if err := transaction.QueryRowContext(ctx, `SELECT version, compatible_min, compatible_max FROM schema_meta WHERE component=$1`, SchemaComponent).Scan(&schemaVersion, &compatibleMinimum, &compatibleMaximum); err != nil {
		_ = transaction.Rollback()
		return fmt.Errorf("schema compatibility metadata missing: %w", err)
	}
	if schemaVersion != SchemaVersion || SchemaVersion < compatibleMinimum || SchemaVersion > compatibleMaximum || schemaVersion < compatibleMinimum || schemaVersion > compatibleMaximum {
		_ = transaction.Rollback()
		return fmt.Errorf("incompatible Ember schema: version=%d compatible=[%d,%d] expected=%d", schemaVersion, compatibleMinimum, compatibleMaximum, SchemaVersion)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("migration commit failed closed: %w", err)
	}
	return nil
}
