GO_IMAGE      ?= golang:1.25
LINT_IMAGE    ?= golangci/golangci-lint:v2.5.0
IMAGE         ?= s3lab:dev
PKGS          ?= ./...
VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

DOCKER_GO = docker run --rm \
	-v "$(CURDIR)":/src \
	-v s3lab-gomod:/go/pkg/mod \
	-v s3lab-gocache:/root/.cache/go-build \
	-w /src $(GO_IMAGE)

.PHONY: test test-race test-integration cover lint fmt tidy build docker-build compat run

test:
	$(DOCKER_GO) go test $(PKGS)

test-race:
	$(DOCKER_GO) go test -race $(PKGS)

test-integration:
	$(DOCKER_GO) go test -race -tags integration ./test/integration/...

cover:
	$(DOCKER_GO) sh -c 'go test -coverprofile=coverage.out ./internal/... && go tool cover -func=coverage.out | tail -1'

lint:
	docker run --rm -v "$(CURDIR)":/src -v s3lab-gomod:/go/pkg/mod -v s3lab-lintcache:/root/.cache -w /src $(LINT_IMAGE) golangci-lint run $(PKGS)

fmt:
	docker run --rm -v "$(CURDIR)":/src -v s3lab-gomod:/go/pkg/mod -v s3lab-lintcache:/root/.cache -w /src $(LINT_IMAGE) golangci-lint fmt ./...

tidy:
	$(DOCKER_GO) go mod tidy

build:
	$(DOCKER_GO) sh -c 'CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/s3lab ./cmd/s3lab'

docker-build:
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE) .

compat:
	docker compose -f test/compat/compose.yaml up --build --abort-on-container-failure --exit-code-from runner; \
	rc=$$?; docker compose -f test/compat/compose.yaml down -v; exit $$rc

run:
	docker compose up --build s3lab
