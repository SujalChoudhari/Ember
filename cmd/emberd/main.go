package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"ember.local/ember/internal/ember"
	"ember.local/ember/internal/persistence/postgres"
)

func openControlPlane(ctx context.Context, databaseURL string, files *ember.FileStore, devMemory bool) (ember.ControlPlane, func() error, error) {
	if files == nil {
		return nil, nil, fmt.Errorf("filesystem provider is required")
	}
	if devMemory {
		return ember.NewStore(files), func() error { return nil }, nil
	}
	if strings.TrimSpace(databaseURL) == "" {
		return nil, nil, fmt.Errorf("PostgreSQL control plane requires --database-url or EMBER_DATABASE_URL")
	}
	store, err := postgres.Open(ctx, databaseURL, files)
	if err != nil {
		return nil, nil, err
	}
	return store, store.Close, nil
}

func main() {
	root := flag.String("blob-root", "", "private Ember Blob provider root (required)")
	authFile := flag.String("auth-file", "", "mode-0600 bearer-token file (required)")
	databaseURL := flag.String("database-url", os.Getenv("EMBER_DATABASE_URL"), "PostgreSQL connection URL (or EMBER_DATABASE_URL)")
	devMemory := flag.Bool("dev-memory-control-plane", false, "explicitly use the in-memory adapter for local contract work only")
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	flag.Parse()
	if *root == "" || *authFile == "" {
		log.Fatal("--blob-root and --auth-file are required; no current-directory fallback")
	}
	files, err := ember.NewFileStore(*root, true)
	if err != nil {
		log.Fatal(err)
	}
	auth, err := ember.LoadAuthFile(*authFile)
	if err != nil {
		log.Fatal(err)
	}
	control, closeControl, err := openControlPlane(context.Background(), *databaseURL, files, *devMemory)
	if err != nil {
		log.Fatal(err)
	}
	defer closeControl()
	server := &http.Server{Addr: *addr, Handler: ember.NewServer(control, auth).Handler()}
	fmt.Printf("emberd listening on %s\n", *addr)
	log.Fatal(server.ListenAndServe())
}
