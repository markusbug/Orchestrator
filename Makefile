VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/markusbug/Orchestrator/daemon/internal/core.Version=$(VERSION)

.PHONY: build test race vet xcompile run-debug clean

build: ## build daemon binary into bin/
	cd daemon && go build -ldflags '$(LDFLAGS)' -o ../bin/orchestrator ./cmd/orchestrator

test: ## run unit and integration tests
	cd daemon && go vet ./... && go test ./...

race: ## run tests with the race detector
	cd daemon && go test -race ./...

xcompile: ## verify cross-compilation for macOS and Linux arm64
	cd daemon && GOOS=darwin GOARCH=arm64 go build -o /dev/null ./... && GOOS=linux GOARCH=arm64 go build -o /dev/null ./...

run-debug: build ## run the daemon in the foreground with the browser debug client
	./bin/orchestrator serve --debug

clean:
	rm -rf bin
