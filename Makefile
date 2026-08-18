.PHONY: fmt-check test test-isolation vet build check

test-isolation:
	@test -n "$(EMBER_TEST_DATABASE_URL)" || (echo 'EMBER_TEST_DATABASE_URL is required for test-isolation' >&2; exit 127)
	go test ./... -count=1
	go test ./... -count=1

fmt-check:
	@test -n "$(shell command -v go 2>/dev/null)" || (echo 'go is required for fmt-check' >&2; exit 127)
	@test -z "$$(gofmt -l .)" || (echo 'gofmt would change files' >&2; gofmt -l .; exit 1)

test:
	go test ./...

vet:
	go vet ./...

build:
	go build ./cmd/emberd ./cmd/ember

check: fmt-check test vet build
