IMAGE ?= quay.io/codeready-toolchain/interactive-labs-console-plugin:latest
CONTAINER_TOOL ?= podman
OC ?= oc
KUSTOMIZE ?= kustomize
NAMESPACE ?= interactive-labs-console-plugin
REGISTRY ?= $(shell $(OC) get route default-route -n openshift-image-registry --template='{{ .spec.host }}' 2>/dev/null)
DEV_IMAGE ?= $(REGISTRY)/$(NAMESPACE)/interactive-labs-console-plugin:latest
PLATFORM ?= linux/amd64
COMMIT ?= $(shell git rev-parse --short HEAD)$(shell git diff --quiet HEAD 2>/dev/null || echo '-dirty')
BUILDTIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

.PHONY: all build test frontend-build frontend-test backend-build backend-test image push-image validate-frontend-bundle validate-deploy validate-demo-catalog validate-catalog-namespace deploy check-registry create-namespace dev-image push-dev-image dev-deploy dev-deploy-with-demo-catalog help

help: ## List available targets
	@printf 'Usage: make <target>\n\nTargets:\n'; \
	awk 'BEGIN { FS = ":.*##" } /^[a-zA-Z0-9][a-zA-Z0-9_-]*:.*##/ { printf "  %-20s %s\n", $$1, $$2 }' $(MAKEFILE_LIST) | LC_ALL=C sort

all: test build ## Run tests and build frontend and backend

build: frontend-build backend-build ## Build frontend and backend

test: frontend-test backend-test ## Run frontend and backend checks

lint: frontend-lint backend-lint ## Lint frontend and backend

frontend-lint:
	npm --prefix frontend ci
	npm --prefix frontend run lint

frontend-build: ## Install frontend dependencies and build frontend
	npm --prefix frontend ci
	npm --prefix frontend run build

frontend-test: ## Type-check, test, and lint frontend
	npm --prefix frontend ci
	npm --prefix frontend run type-check
	npm --prefix frontend test -- --coverage
	npm --prefix frontend run lint

backend-lint:
	cd backend && golangci-lint run

backend-build: ## Build backend server
	cd backend && go build -ldflags "-X main.commit=$(git rev-parse --short HEAD) -X main.buildtime=$(date -u +%Y-%m-%dT%H:%M:%SZ)" ./cmd/server

backend-test: ## Test and vet backend
	cd backend && go test ./...
	cd backend && go vet ./...

validate-frontend-bundle: ## Validate exposed page module initialization
	npm --prefix frontend run build
	@! grep -Fq 'reexport safe' frontend/dist/exposed-main.js

image: ## Build container image
	$(CONTAINER_TOOL) build --no-cache --platform=${PLATFORM} \
	  --build-arg COMMIT=$(COMMIT) \
      --build-arg BUILDTIME=$(BUILDTIME) \
	  -f Containerfile -t $(IMAGE) .

push-image: image ## Build and push default image
	$(CONTAINER_TOOL) push $(IMAGE)

validate-deploy: ## Validate Kustomize deployment manifests
	$(OC) kustomize deploy/base | $(OC) apply --dry-run=client -f -

validate-demo-catalog: ## Validate demo catalog deployment manifests
	$(OC) kustomize deploy/demo | $(OC) apply --dry-run=client -f -

validate-catalog-namespace: ## Validate catalog ConfigMap namespace
	@for dir in deploy/base deploy/demo; do $(KUSTOMIZE) build "$$dir" | grep -A1 -Fx '  name: interactive-labs-catalog' | grep -Fx '  namespace: interactive-labs-console-plugin' >/dev/null || exit 1; done

deploy: ## Apply deployment manifests
	$(OC) apply -k deploy/base

expose-registry: ## Expose OpenShift registry
	$(OC) patch configs.imageregistry.operator.openshift.io/cluster --patch '{"spec":{"defaultRoute":true}}' --type=merge

check-registry: ## Verify OpenShift registry access
	@test -n "$(REGISTRY)" || { echo "OpenShift image registry route unavailable."; exit 1; }
	@$(OC) whoami >/dev/null
	@$(OC) whoami --show-token >/dev/null

create-namespace: ## Create plugin namespace
	$(OC) apply -f deploy/base/namespace.yaml

dev-image: create-namespace ## Build development image
	$(CONTAINER_TOOL) build --no-cache --platform=${PLATFORM} \
	  --build-arg COMMIT=$(COMMIT) \
      --build-arg BUILDTIME=$(BUILDTIME) \
      -f Containerfile -t $(DEV_IMAGE) .

push-dev-image: check-registry dev-image ## Build and push development image
	$(CONTAINER_TOOL) push $(DEV_IMAGE) --creds="unused:$$( $(OC) whoami --show-token )" --tls-verify=false

dev-deploy: push-dev-image ## Build, push, and deploy development image
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	cp -R deploy/base/. "$$tmp"; \
	cd "$$tmp"; \
	$(KUSTOMIZE) edit set image interactive-labs-console-plugin=$(DEV_IMAGE); \
	$(KUSTOMIZE) build . | $(OC) apply -f - && \
	$(OC) rollout restart deployment/interactive-labs -n $(NAMESPACE) && \
	$(OC) rollout status deployment/interactive-labs -n $(NAMESPACE)
	$(OC) patch consoles.operator.openshift.io cluster --type=json --patch '[{"op":"add","path":"/spec/plugins/-","value":"interactive-labs-console-plugin"}]'

dev-deploy-with-demo-catalog: push-dev-image ## Build, push, and deploy development image with demo catalog
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	cp -R deploy/base/. "$$tmp"; \
	cp deploy/demo/catalog.yaml "$$tmp/catalog.yaml"; \
	cd "$$tmp"; \
	$(KUSTOMIZE) edit set image interactive-labs-console-plugin=$(DEV_IMAGE); \
	$(KUSTOMIZE) build . | $(OC) apply -f - && \
	$(OC) rollout restart deployment/interactive-labs -n $(NAMESPACE) && \
	$(OC) rollout status deployment/interactive-labs -n $(NAMESPACE)
	$(OC) patch consoles.operator.openshift.io cluster --type=json --patch '[{"op":"add","path":"/spec/plugins/-","value":"interactive-labs-console-plugin"}]'
