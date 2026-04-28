.PHONY: all build test clean docker install lint fmt help \
	docker-nethermind docker-nethermind-test test-nethermind-oracle smoke-nethermind

# Binary name
BINARY=state-actor
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-X main.Version=$(VERSION)"

# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOTEST=$(GOCMD) test
GOCLEAN=$(GOCMD) clean
GOGET=$(GOCMD) get
GOMOD=$(GOCMD) mod
GOFMT=$(GOCMD) fmt

# Default target
all: build

## build: Build the binary
build:
	$(GOBUILD) $(LDFLAGS) -o $(BINARY) .

## install: Install to $GOPATH/bin
install:
	$(GOCMD) install $(LDFLAGS) .

## test: Run tests
test:
	$(GOTEST) -v ./...

## test-race: Run tests with race detector
test-race:
	$(GOTEST) -race -v ./...

## test-coverage: Run tests with coverage
test-coverage:
	$(GOTEST) -coverprofile=coverage.out ./...
	$(GOCMD) tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

## bench: Run benchmarks
bench:
	$(GOTEST) -bench=. -benchmem ./generator

## lint: Run linter
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed, running go vet instead"; \
		$(GOCMD) vet ./...; \
	fi

## fmt: Format code
fmt:
	$(GOFMT) ./...

## clean: Clean build artifacts
clean:
	$(GOCLEAN)
	rm -f $(BINARY)
	rm -f coverage.out coverage.html
	rm -rf dist/

## docker: Build Docker image
docker:
	docker build -t state-actor:latest .
	docker build -t state-actor:$(VERSION) .

## docker-nethermind: Build the Nethermind-capable image (cgo+grocksdb+rocksdb-from-source)
docker-nethermind:
	docker build -f Dockerfile.nethermind -t state-actor-nethermind:latest -t state-actor-nethermind:$(VERSION) .

## docker-nethermind-test: Build the builder stage so we can run cgo_neth go tests inside it
docker-nethermind-test:
	docker build -f Dockerfile.nethermind --target builder -t state-actor-nethermind-builder:latest .

## test-nethermind-oracle: Run the Tier 2 differential oracle (3 CCD-cited golden hashes)
test-nethermind-oracle: docker-nethermind-test
	docker run --rm --entrypoint bash state-actor-nethermind-builder:latest \
	  -c 'cd /app && go test -tags cgo_neth -run TestDifferentialOracle -v ./client/nethermind/...'

## smoke-nethermind: End-to-end smoke — generate a small DB, boot Nethermind 1.37.0, send 100 dev-mode txs
##   Usage: make smoke-nethermind ACCOUNTS=1000 CONTRACTS=100
ACCOUNTS ?= 1000
CONTRACTS ?= 100
SEED ?= 42
SA_DB ?= /tmp/sa-neth-smoke
smoke-nethermind: docker-nethermind
	rm -rf $(SA_DB) && mkdir -p $(SA_DB)
	docker run --rm \
	  -v $(SA_DB):/data \
	  -v $(PWD)/client/nethermind/testdata:/test:ro \
	  state-actor-nethermind:latest \
	  --client=nethermind --db=/data \
	  --accounts=$(ACCOUNTS) --contracts=$(CONTRACTS) --seed=$(SEED) \
	  --genesis=/test/genesis-funded.json --verbose
	bash $(PWD)/client/nethermind/testdata/validate-big-db.sh $(SA_DB)

## tidy: Tidy go modules
tidy:
	$(GOMOD) tidy

## deps: Download dependencies
deps:
	$(GOMOD) download

## example: Run example generation
example:
	./$(BINARY) \
		--db /tmp/example-chaindata \
		--genesis examples/test-genesis.json \
		--accounts 1000 \
		--contracts 500 \
		--max-slots 100 \
		--seed 42 \
		--verbose \
		--benchmark
	@echo ""
	@echo "Example database created at /tmp/example-chaindata"
	@du -sh /tmp/example-chaindata

## help: Show this help
help:
	@echo "State Actor - Ethereum State Generator"
	@echo ""
	@echo "Usage: make [target]"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
