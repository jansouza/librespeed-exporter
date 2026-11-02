GO            ?= go
GOLANGCI_LINT ?= golangci-lint
DOCKER        ?= docker

BINARY  := librespeed-exporter
PKG     := ./cmd/librespeed-exporter
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
VERSION := $(patsubst v%,%,$(VERSION))
REVISION := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)

VERSION_PKG := github.com/prometheus/common/version
LDFLAGS := -s -w \
	-X $(VERSION_PKG).Version=$(VERSION) \
	-X $(VERSION_PKG).Revision=$(REVISION) \
	-X $(VERSION_PKG).Branch=$(shell git rev-parse --abbrev-ref HEAD 2>/dev/null || echo unknown) \
	-X $(VERSION_PKG).BuildUser=$(shell whoami 2>/dev/null || id -u)@$(shell hostname) \
	-X $(VERSION_PKG).BuildDate=$(shell date -u +%Y%m%d-%H:%M:%S)

# Keep in sync with the LIBRESPEED_CLI_VERSION default in the Dockerfile.
LIBRESPEED_CLI_VERSION ?= 1.0.14
PROMETHEUS_IMAGE       ?= prom/prometheus:v3.15.1
IMAGE                  ?= librespeed-exporter:$(VERSION)

CACHE_DIR := .cache
CLI_DIR   := $(CACHE_DIR)/librespeed-cli-$(LIBRESPEED_CLI_VERSION)
COMPOSE   := $(DOCKER) compose -f test/integration/compose.yml
PROMTOOL  := $(DOCKER) run --rm -i -v $(CURDIR):/work:ro -w /work --entrypoint promtool $(PROMETHEUS_IMAGE)

.PHONY: all build test lint fmt integration docker check-deploy clean

all: lint test build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY) $(PKG)

test:
	$(GO) test -race ./...

lint:
	$(GOLANGCI_LINT) run

fmt:
	$(GOLANGCI_LINT) fmt

$(CLI_DIR)/librespeed-cli:
	./scripts/fetch-librespeed-cli.sh $(LIBRESPEED_CLI_VERSION) $$($(GO) env GOOS) $$($(GO) env GOARCH) $(CLI_DIR)

# Runs the exporter with the real librespeed-cli against a LibreSpeed
# container, then validates the scraped metrics with promtool.
integration: $(CLI_DIR)/librespeed-cli
	$(COMPOSE) up -d --wait
	LIBRESPEED_CLI=$(CURDIR)/$(CLI_DIR)/librespeed-cli \
	METRICS_OUT=$(CURDIR)/$(CACHE_DIR)/metrics.txt \
	$(GO) test -tags integration -count=1 -v ./test/integration/...; \
	status=$$?; $(COMPOSE) down; exit $$status
	$(PROMTOOL) check metrics < $(CACHE_DIR)/metrics.txt

docker:
	$(DOCKER) build \
		--build-arg VERSION=$(VERSION) \
		--build-arg REVISION=$(REVISION) \
		--build-arg LIBRESPEED_CLI_VERSION=$(LIBRESPEED_CLI_VERSION) \
		-t $(IMAGE) .

# Validates the example alert rules and Prometheus configuration.
check-deploy:
	$(PROMTOOL) check rules deploy/prometheus/alerts.yml
	$(PROMTOOL) check config --syntax-only deploy/compose/prometheus.yml

clean:
	rm -rf $(BINARY) $(CACHE_DIR) dist
