BINARY_NAME=server
DASHBOARD_BINARY_NAME=dashboard
CLIENT_BINARY_NAME=soph
IMAGE_NAME ?= sopholeth/node:local

# Burn-in cluster seeds — useful for `make dashboard-run-burnin` smoke tests.
BURNIN_SEEDS ?= localhost:8091,localhost:8092,localhost:8093

.PHONY: build build-dashboard build-soph probesim-run run dashboard-run-burnin test test-race check-public-release clean docker-build docker-run docker-compose-up docker-compose-down

build:
	go build -o bin/$(BINARY_NAME) ./cmd/server
	go build -o bin/$(DASHBOARD_BINARY_NAME) ./cmd/dashboard
	go build -o bin/$(CLIENT_BINARY_NAME) ./cmd/soph
	go build -o bin/probesim ./cmd/probesim

build-soph:
	go build -o bin/$(CLIENT_BINARY_NAME) ./cmd/soph

build-dashboard:
	go build -o bin/$(DASHBOARD_BINARY_NAME) ./cmd/dashboard

# Gentle probe chatter on the public testnet; Ctrl-C prints a fleet summary.
probesim-run:
	go run ./cmd/probesim

dashboard-run-burnin: build-dashboard
	./bin/$(DASHBOARD_BINARY_NAME) \
		--seeds=$(BURNIN_SEEDS) \
		--state-dir=$(CURDIR)/.dashboard-state \
		--listen=127.0.0.1:18181 \
		--internal-addr=127.0.0.1:18182 \
		--poll-interval=30s

run: build
	./bin/$(BINARY_NAME)

# Omega tests fsync every durable write. On a spinning disk that dominates
# the run, so keep test temp directories in memory when /dev/shm exists.
TEST_TMPDIR := $(shell [ -d /dev/shm ] && echo /dev/shm || echo $${TMPDIR:-/tmp})

test:
	TMPDIR=$(TEST_TMPDIR) go test ./...

test-race:
	TMPDIR=$(TEST_TMPDIR) go test -race ./...

# The expected fingerprint comes from the authority's independent release
# record, not from the same checkout being verified. Empty is an error.
check-public-release: export OMEGA_EXPECTED_SHA256 := $(OMEGA_EXPECTED_SHA256)
check-public-release:
	go test ./internal/discovery -run '^TestPublicReleaseBundle$$' -count=1

clean:
	go clean
	rm -rf bin/

docker-build:
	docker build -t $(IMAGE_NAME) .

docker-run:
	docker run --rm -p 127.0.0.1:8080:8080 -e NODE_NETWORK=private $(IMAGE_NAME)

docker-compose-up:
	docker compose up --build

docker-compose-down:
	docker compose down
