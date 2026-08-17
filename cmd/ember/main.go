package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func main() {
	base := flag.String("base", "http://127.0.0.1:8080", "Ember HTTP endpoint")
	token := flag.String("token", "", "bearer token (prefer an external mode-0600 auth file in scripts)")
	flag.Parse()
	if *token == "" || flag.NArg() < 1 { fmt.Fprintln(os.Stderr, "usage: ember -token TOKEN [group-create|bucket-create|object-put|object-get|object-head|object-delete] ..."); os.Exit(2) }
	command := flag.Arg(0); var method, path string; var body io.Reader; headers := map[string]string{"Authorization": "Bearer " + *token}
	switch command {
	case "group-create":
		if flag.NArg() != 5 { fail("group-create INSTANCE TENANT SUBSCRIPTION NAME") }; path = fmt.Sprintf("/api/v1/instances/%s/tenants/%s/subscriptions/%s/resourceGroups", flag.Arg(1), flag.Arg(2), flag.Arg(3)); payload := map[string]string{"name": flag.Arg(4)}; body = jsonBody(payload); method = http.MethodPost; headers["Idempotency-Key"] = "cli-group-" + flag.Arg(4)
	case "bucket-create":
		if flag.NArg() != 3 { fail("bucket-create GROUP_ID NAME") }; path = "/api/v1/resourceGroups/" + flag.Arg(1) + "/providers/Ember.Blob/buckets"; body = jsonBody(map[string]string{"name": flag.Arg(2)}); method = http.MethodPost; headers["Idempotency-Key"] = "cli-bucket-" + flag.Arg(2)
	case "object-put":
		if flag.NArg() != 4 { fail("object-put BUCKET_ID KEY FILE") }; b, err := os.ReadFile(flag.Arg(3)); if err != nil { fail(err.Error()) }; h := sha256.Sum256(b); path = "/api/v1/data/buckets/" + flag.Arg(1) + "/objects/" + flag.Arg(2); body = bytes.NewReader(b); method = http.MethodPut; headers["Content-Length"] = fmt.Sprintf("%d", len(b)); headers["Content-Digest"] = "sha-256=\"" + hex.EncodeToString(h[:]) + "\""; headers["Idempotency-Key"] = "cli-object-" + flag.Arg(2)
	case "object-get", "object-head", "object-delete":
		if flag.NArg() != 3 { fail(command + " BUCKET_ID KEY") }; path = "/api/v1/data/buckets/" + flag.Arg(1) + "/objects/" + flag.Arg(2); method = map[string]string{"object-get": http.MethodGet, "object-head": http.MethodHead, "object-delete": http.MethodDelete}[command]; if method == http.MethodDelete { headers["Idempotency-Key"] = "cli-delete-" + flag.Arg(2) }
	default: fail("unknown command")
	}
	req, err := http.NewRequest(method, strings.TrimRight(*base, "/")+path, body); if err != nil { fail(err.Error()) }; for k, v := range headers { req.Header.Set(k, v) }
	resp, err := http.DefaultClient.Do(req); if err != nil { fail(err.Error()) }; defer resp.Body.Close(); out, _ := io.ReadAll(resp.Body); if resp.StatusCode >= 400 { fmt.Fprintf(os.Stderr, "%s\n", out); os.Exit(1) }; if command == "object-get" { os.Stdout.Write(out) } else if len(out) > 0 { fmt.Println(string(out)) }
}

func jsonBody(v any) io.Reader { b, _ := json.Marshal(v); return bytes.NewReader(b) }
func fail(msg string) { fmt.Fprintln(os.Stderr, msg); os.Exit(2) }
