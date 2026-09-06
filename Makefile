# Each binary has its own tag series: v* for the daemon, relay-v* for the
# relay (the app uses app-v*). Matching on the prefix keeps one series from
# stamping another; outside a git checkout the version is "dev".
VERSION ?= $(shell git describe --tags --match 'v*' --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/markusbug/Orchestrator/daemon/internal/buildinfo.Version=$(VERSION)
RELAY_VERSION ?= $(shell (git describe --tags --match 'relay-v*' --always --dirty 2>/dev/null || echo dev) | sed 's/^relay-//')
RELAY_LDFLAGS := -s -w -X github.com/markusbug/Orchestrator/daemon/internal/buildinfo.Version=$(RELAY_VERSION)
LOAD_HOSTS ?= 1000

.PHONY: build test race vet xcompile run-debug clean app-check app-live build-relay build-relay-linux run-relay-dev relay-load

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

app-check: ## format check, analyze, and test the Flutter app
	cd app && dart format --output=none --set-exit-if-changed lib test && flutter analyze && flutter test

app-live: ## run the app's live test against a daemon (needs ORCH_LIVE_PORT, ORCH_LIVE_CODE, ORCH_LIVE_FP)
	cd app && flutter test test/live_test.dart

build-relay: ## build the relay binary into bin/
	cd daemon && CGO_ENABLED=0 go build -trimpath -ldflags '$(RELAY_LDFLAGS)' -o ../bin/relay ./cmd/relay

build-relay-linux: ## release-style linux/amd64 and linux/arm64 relay binaries into dist/
	cd daemon && for a in amd64 arm64; do CGO_ENABLED=0 GOOS=linux GOARCH=$$a go build -trimpath -ldflags '$(RELAY_LDFLAGS)' -o ../dist/relay_linux_$$a ./cmd/relay || exit 1; done
	cd dist && sha256sum relay_linux_* > SHA256SUMS

run-relay-dev: build-relay ## self-signed relay on 127.0.0.1:8443 with metrics on 127.0.0.1:9100 (per-IP limit lifted for relay-load)
	./bin/relay --dev --dev-cert bin/relay-dev-cert.pem --domain localhost --listen 127.0.0.1:8443 --metrics 127.0.0.1:9100 --log-format text -v --conns-per-minute-per-ip 1000000 --max-peeking-per-ip 100000

relay-load: ## open LOAD_HOSTS fake hosts against the local dev relay (see cmd/relayload)
	cd daemon && go run -tags live ./cmd/relayload -relay https://localhost:8443 -insecure -hosts $(LOAD_HOSTS) -rate 200 -duration 1m

clean:
	rm -rf bin dist
