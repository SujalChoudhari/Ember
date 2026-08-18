package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"ember.local/ember/internal/ember"
	_ "github.com/lib/pq"
)

func Open(ctx context.Context, databaseURL string, fileStore *ember.FileStore) (*Store, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("PostgreSQL database URL is required")
	}
	if fileStore == nil {
		return nil, fmt.Errorf("filesystem provider is required")
	}
	if err := DefaultConfig().Validate(); err != nil {
		return nil, err
	}
	database, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = database.Close()
		}
	}()
	configuration := DefaultConfig()
	database.SetMaxOpenConns(configuration.MaxConnections)
	database.SetMaxIdleConns(configuration.MaxConnections)
	database.SetConnMaxLifetime(30 * time.Minute)
	connectContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := database.PingContext(connectContext); err != nil {
		return nil, fmt.Errorf("PostgreSQL connection failed: %w", err)
	}
	if _, err := database.ExecContext(connectContext, `SELECT set_config('statement_timeout', $1, false), set_config('idle_in_transaction_session_timeout', $2, false)`, configuration.StatementTimeout, configuration.IdleTxTimeout); err != nil {
		return nil, fmt.Errorf("PostgreSQL timeout policy failed: %w", err)
	}
	if err := ApplyMigrations(connectContext, database, Migrations()); err != nil {
		return nil, err
	}
	store, err := newStore(database, fileStore)
	if err != nil {
		return nil, err
	}
	closeOnError = false
	return store, nil
}
