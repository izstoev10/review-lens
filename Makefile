# review-lens — developer tasks. Run `make` (or `make help`) to list targets.

BINARY  := review-lens
BIN_DIR := bin
BIN     := $(BIN_DIR)/$(BINARY)
PKGS    := ./...

.DEFAULT_GOAL := help

## help: list the available targets
.PHONY: help
help:
	@echo "review-lens — make targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed -E 's/^## /  /'

## build: compile the CLI to bin/review-lens
.PHONY: build
build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN) .

## install: install review-lens onto your GOPATH/bin
.PHONY: install
install:
	go install .

## run: run the CLI (pass args via ARGS, e.g. `make run ARGS="pr 123"`)
.PHONY: run
run:
	go run . $(ARGS)

## test: run all tests
.PHONY: test
test:
	go test $(PKGS)

## vet: run go vet
.PHONY: vet
vet:
	go vet $(PKGS)

## fmt: format all Go files in place
.PHONY: fmt
fmt:
	gofmt -w .

## check: verify formatting + vet without changing anything (pre-push gate)
.PHONY: check
check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:"; echo "$$unformatted"; exit 1; \
	fi
	go vet $(PKGS)

## ci: reproduce the GitHub CI gate locally (vet, build, test)
.PHONY: ci
ci: vet build test

## tidy: tidy go.mod / go.sum
.PHONY: tidy
tidy:
	go mod tidy

## clean: remove build artifacts
.PHONY: clean
clean:
	rm -rf $(BIN_DIR)
