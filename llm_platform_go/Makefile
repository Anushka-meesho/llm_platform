# llm_platform_go — common dev & deploy tasks.
# Run `make help` for the list. See docs/DEPLOY.md for production details.

BINDIR    ?= bin
IMAGE     ?= llm-platform:latest
GOFLAGS   ?= -trimpath
LDFLAGS   ?= -s -w

.DEFAULT_GOAL := help

## help: list available targets
.PHONY: help
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //'

## build: compile all four binaries (static, no cgo) into ./bin
.PHONY: build
build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINDIR)/llm-platform ./cmd/server
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINDIR)/migrate     ./cmd/migrate
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINDIR)/bootstrap   ./cmd/bootstrap
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINDIR)/issue-token ./cmd/issue-token

## run: start the API server (loads .env; listens on :8000)
.PHONY: run
run:
	go run ./cmd/server

## test: run the full test suite
.PHONY: test
test:
	go test ./...

## vet: run go vet across all packages
.PHONY: vet
vet:
	go vet ./...

## fmt: format all Go source
.PHONY: fmt
fmt:
	gofmt -w .

## tidy: sync go.mod/go.sum
.PHONY: tidy
tidy:
	go mod tidy

## check: fmt + vet + test (pre-commit gate)
.PHONY: check
check: fmt vet test

## migrate: apply the DB schema out-of-band (reads .env / env for DB_DRIVER)
.PHONY: migrate
migrate:
	go run ./cmd/migrate

## bootstrap: first-run setup — gen JWT secret, validate, migrate, mint admin
.PHONY: bootstrap
bootstrap:
	go run ./cmd/bootstrap -write-env -issue-admin

## docker-build: build the production (API-only) container image
.PHONY: docker-build
docker-build:
	docker build -t $(IMAGE) .

## docker-run: run the image locally on :8000 (pass env with --env-file .env)
.PHONY: docker-run
docker-run:
	docker run --rm -p 8000:8000 --env-file .env $(IMAGE)

## clean: remove build outputs
.PHONY: clean
clean:
	rm -rf $(BINDIR)
