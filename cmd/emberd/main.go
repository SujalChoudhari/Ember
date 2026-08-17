package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"

	"ember.local/ember/internal/ember"
)

func main() {
	root := flag.String("blob-root", "", "private Ember Blob provider root (required)")
	authFile := flag.String("auth-file", "", "mode-0600 bearer-token file (required)")
	devMemory := flag.Bool("dev-memory-control-plane", false, "explicitly use the in-memory adapter; PostgreSQL wiring is required for production")
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	flag.Parse()
	if *root == "" || *authFile == "" { log.Fatal("--blob-root and --auth-file are required; no current-directory fallback") }
	if !*devMemory { log.Fatal("PostgreSQL-backed control plane is required; use --dev-memory-control-plane only for local contract work") }
	files, err := ember.NewFileStore(*root, true)
	if err != nil { log.Fatal(err) }
	auth, err := ember.LoadAuthFile(*authFile)
	if err != nil { log.Fatal(err) }
	store := ember.NewStore(files)
	server := &http.Server{Addr: *addr, Handler: ember.NewServer(store, auth).Handler()}
	fmt.Printf("emberd listening on %s\n", *addr)
	log.Fatal(server.ListenAndServe())
}
