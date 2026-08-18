package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"ember.local/ember/internal/ember"
)

func main() {
	blobRootPath := flag.String("blob-root", "", "private Ember Blob provider root (required)")
	authFilePath := flag.String("auth-file", "", "mode-0600 bearer-token file (required)")
	databaseURL := flag.String("database-url", os.Getenv("EMBER_DATABASE_URL"), "PostgreSQL connection URL (or EMBER_DATABASE_URL)")
	useDevelopmentMemory := flag.Bool("dev-memory-control-plane", false, "explicitly use the in-memory adapter for local contract work only")
	listenAddress := flag.String("addr", "127.0.0.1:8080", "listen address")
	flag.Parse()
	if *blobRootPath == "" || *authFilePath == "" {
		log.Fatal("--blob-root and --auth-file are required; no current-directory fallback")
	}
	fileStore, err := ember.NewFileStore(*blobRootPath, true)
	if err != nil {
		log.Fatal(err)
	}
	auth, err := ember.LoadAuthFile(*authFilePath)
	if err != nil {
		log.Fatal(err)
	}
	controlPlane, closeControlPlane, err := openControlPlane(context.Background(), *databaseURL, fileStore, *useDevelopmentMemory)
	if err != nil {
		log.Fatal(err)
	}
	defer closeControlPlane()
	server := &http.Server{Addr: *listenAddress, Handler: ember.NewServer(controlPlane, auth).Handler()}
	fmt.Printf("emberd listening on %s\n", *listenAddress)
	log.Fatal(server.ListenAndServe())
}
