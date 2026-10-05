VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMG     ?= ghcr.io/aireet/kube-bmc:$(VERSION)
GOBIN   ?= $(shell go env GOPATH)/bin
CONTROLLER_GEN ?= $(GOBIN)/controller-gen

.PHONY: all
all: ui build

##@ Development

.PHONY: generate
generate: ## Generate deepcopy code and the CRD, and sync it into the Helm chart.
	$(CONTROLLER_GEN) object:headerFile=hack/boilerplate.go.txt paths=./api/...
	$(CONTROLLER_GEN) crd paths=./api/... output:crd:artifacts:config=config/crd
	cp config/crd/*.yaml charts/kube-bmc/crds/

.PHONY: ui
ui: ## Build the dashboard into web/dist.
	cd ui && npm ci --no-audit --no-fund && npm run build

.PHONY: build
build: ## Build the kube-bmc binary.
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/kube-bmc ./cmd/kube-bmc

.PHONY: test
test: ## Run unit tests.
	go test -race -count=1 ./...

.PHONY: lint
lint: ## Run golangci-lint and the UI type checker.
	golangci-lint run
	cd ui && npm run typecheck

.PHONY: demo
demo: ui build ## Run the dashboard with a synthetic fleet on http://localhost:8080.
	./bin/kube-bmc server --demo

.PHONY: dev
dev: ## Run the UI dev server against a demo backend (hot reload).
	go run ./cmd/kube-bmc server --demo & cd ui && npm run dev

##@ Release

.PHONY: image
image: ## Build the container image.
	docker build --build-arg VERSION=$(VERSION) -t $(IMG) .

.PHONY: manifests
manifests: ## Render the plain-YAML install manifest.
	@mkdir -p dist
	helm template kube-bmc charts/kube-bmc --namespace kube-bmc-system --include-crds > dist/install.yaml
	@echo "---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: kube-bmc-system" >> dist/install.yaml

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make <target>\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
