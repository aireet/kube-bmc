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

LDFLAGS := -s -w -X main.version=$(VERSION)
PLUGIN_PLATFORMS ?= linux/amd64 linux/arm64

.PHONY: build
build: ## Build kube-bmc and kubectl-bmc into bin/.
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/kube-bmc ./cmd/kube-bmc
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/kubectl-bmc ./cmd/kubectl-bmc

.PHONY: test
test: ## Run unit tests.
	go test -race -count=1 ./...

.PHONY: e2e
e2e: ## Run the end-to-end test in a temporary kind cluster (requires docker, kind, helm).
	test/e2e/run.sh

.PHONY: lint
lint: ## Run golangci-lint and the UI type checker.
	golangci-lint run
	cd ui && npm run typecheck

.PHONY: dev
dev: ## Run the UI dev server with hot reload against a port-forwarded server on :8080.
	cd ui && KUBE_BMC_API=http://127.0.0.1:8080 npm run dev

##@ Release

.PHONY: image
image: ## Build the container image.
	docker build --build-arg VERSION=$(VERSION) -t $(IMG) .

.PHONY: plugins
plugins: ## Cross-compile kubectl-bmc archives into dist/.
	@mkdir -p dist
	@for p in $(PLUGIN_PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		out=dist/kubectl-bmc_$${os}_$${arch}; mkdir -p $$out; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$out/kubectl-bmc ./cmd/kubectl-bmc || exit 1; \
		cp LICENSE $$out/; tar -C $$out -czf $$out.tar.gz . && rm -rf $$out; \
	done
	@cd dist && sha256sum kubectl-bmc_*.tar.gz > kubectl-bmc_checksums.txt

.PHONY: manifests
manifests: ## Render the plain-YAML install manifest.
	@mkdir -p dist
	helm template kube-bmc charts/kube-bmc --namespace kube-bmc-system --include-crds > dist/install.yaml
	@echo "---\napiVersion: v1\nkind: Namespace\nmetadata:\n  name: kube-bmc-system" >> dist/install.yaml

.PHONY: help
help: ## Show this help.
	@awk 'BEGIN {FS = ":.*##"; printf "Usage: make <target>\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)
