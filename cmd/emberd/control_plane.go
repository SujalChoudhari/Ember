package main

import (
	"context"
	"fmt"
	"strings"

	"ember.local/ember/internal/ember"
	"ember.local/ember/internal/persistence/postgres"
)

func openControlPlane(ctx context.Context, databaseURL string, fileStore *ember.FileStore, useDevelopmentMemory bool) (ember.ControlPlane, func() error, error) {
	if fileStore == nil {
		return nil, nil, fmt.Errorf("filesystem provider is required")
	}
	if useDevelopmentMemory {
		return ember.NewStore(fileStore), func() error { return nil }, nil
	}
	if strings.TrimSpace(databaseURL) == "" {
		return nil, nil, fmt.Errorf("PostgreSQL control plane requires --database-url or EMBER_DATABASE_URL")
	}
	store, err := postgres.Open(ctx, databaseURL, fileStore)
	if err != nil {
		return nil, nil, err
	}
	return store, store.Close, nil
}
