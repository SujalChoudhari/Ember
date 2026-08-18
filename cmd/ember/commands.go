package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func runCommand(baseURL, bearerToken string, commandArguments []string) {
	if bearerToken == "" || len(commandArguments) < 1 {
		fail("usage: ember -token TOKEN [group-create|bucket-create|object-put|object-get|object-head|object-delete] ...")
	}
	commandName := commandArguments[0]
	var method string
	var requestPath string
	var requestBody io.Reader
	headers := map[string]string{"Authorization": "Bearer " + bearerToken}
	switch commandName {
	case "group-create":
		if len(commandArguments) != 5 {
			fail("group-create INSTANCE TENANT SUBSCRIPTION NAME")
		}
		requestPath = fmt.Sprintf("/api/v1/instances/%s/tenants/%s/subscriptions/%s/resourceGroups", commandArguments[1], commandArguments[2], commandArguments[3])
		requestBody = jsonBody(map[string]string{"name": commandArguments[4]})
		method = http.MethodPost
		headers["Idempotency-Key"] = "cli-group-" + commandArguments[4]
	case "bucket-create":
		if len(commandArguments) != 3 {
			fail("bucket-create GROUP_ID NAME")
		}
		requestPath = "/api/v1/resourceGroups/" + commandArguments[1] + "/providers/Ember.Blob/buckets"
		requestBody = jsonBody(map[string]string{"name": commandArguments[2]})
		method = http.MethodPost
		headers["Idempotency-Key"] = "cli-bucket-" + commandArguments[2]
	case "object-put":
		if len(commandArguments) != 4 {
			fail("object-put BUCKET_ID KEY FILE")
		}
		payload, err := os.ReadFile(commandArguments[3])
		if err != nil {
			fail(err.Error())
		}
		payloadDigest := sha256.Sum256(payload)
		requestPath = "/api/v1/data/buckets/" + commandArguments[1] + "/objects/" + commandArguments[2]
		requestBody = bytes.NewReader(payload)
		method = http.MethodPut
		headers["Content-Length"] = fmt.Sprintf("%d", len(payload))
		headers["Content-Digest"] = "sha-256=\"" + hex.EncodeToString(payloadDigest[:]) + "\""
		headers["Idempotency-Key"] = "cli-object-" + commandArguments[2]
	case "object-get", "object-head", "object-delete":
		if len(commandArguments) != 3 {
			fail(commandName + " BUCKET_ID KEY")
		}
		requestPath = "/api/v1/data/buckets/" + commandArguments[1] + "/objects/" + commandArguments[2]
		method = map[string]string{"object-get": http.MethodGet, "object-head": http.MethodHead, "object-delete": http.MethodDelete}[commandName]
		if method == http.MethodDelete {
			headers["Idempotency-Key"] = "cli-delete-" + commandArguments[2]
		}
	default:
		fail("unknown command")
	}
	request, err := http.NewRequest(method, strings.TrimRight(baseURL, "/")+requestPath, requestBody)
	if err != nil {
		fail(err.Error())
	}
	for headerName, headerValue := range headers {
		request.Header.Set(headerName, headerValue)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		fail(err.Error())
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(response.Body)
	if response.StatusCode >= 400 {
		fmt.Fprintf(os.Stderr, "%s\n", responseBody)
		os.Exit(1)
	}
	if commandName == "object-get" {
		_, _ = os.Stdout.Write(responseBody)
	} else if len(responseBody) > 0 {
		fmt.Println(string(responseBody))
	}
}

func jsonBody(value any) io.Reader {
	body, _ := json.Marshal(value)
	return bytes.NewReader(body)
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(2)
}
