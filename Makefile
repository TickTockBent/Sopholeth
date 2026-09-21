BINARY_NAME=server
OMEGA_BINARY_NAME=omega
DASHBOARD_BINARY_NAME=dashboard
CLIENT_BINARY_NAME=soph
IMAGE_NAME ?= sopholeth/node:local

# Burn-in cluster seeds — useful for `make dashboard-run-burnin` smoke tests.
BURNIN_SEEDS ?= localhost:8091,localhost:8092,localhost:8093

.PHONY: build build-omega build-dashboard build-soph run dashboard-run-burnin test clean docker-build docker-run docker-compose-up docker-compose-down

build:
	go build -o bin/$(BINARY_NAME) ./cmd/server
	go build -o bin/$(OMEGA_BINARY_NAME) ./cmd/omega
	go build -o bin/$(DASHBOARD_BINARY_NAME) ./cmd/dashboard
	go build -o bin/$(CLIENT_BINARY_NAME) ./cmd/soph

build-omega:
	go build -o bin/$(OMEGA_BINARY_NAME) ./cmd/omega

build-soph:
	go build -o bin/$(CLIENT_BINARY_NAME) ./cmd/soph

build-dashboard:
	go build -o bin/$(DASHBOARD_BINARY_NAME) ./cmd/dashboard

dashboard-run-burnin: build-dashboard
	./bin/$(DASHBOARD_BINARY_NAME) \
		--seeds=$(BURNIN_SEEDS) \
		--state-dir=$(CURDIR)/.dashboard-state \
		--listen=127.0.0.1:18181 \
		--internal-addr=127.0.0.1:18182 \
		--poll-interval=30s

run: build
	./bin/$(BINARY_NAME)

test:
	go test ./...

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
