.PHONY: fmt-check test vet build check

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
