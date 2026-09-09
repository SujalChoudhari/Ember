.PHONY: build test package install smoke

build:
	go build -buildvcs=false -trimpath -o bin/ember ./cmd/ember

test:
	go test ./...

package:
	./scripts/ember-package.sh

install:
	./scripts/ember-install.sh

smoke: package
	./scripts/ember-smoke.sh
