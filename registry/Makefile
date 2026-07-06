# Registry operator — provisions per-tenant Harbor registries via CRDs.
# Layout follows the KeyVault operator (crds/keyvault) conventions.

IMG ?= e20449/registry-provisioner:latest
CONTAINER_TOOL ?= docker

# Tooling
LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen
CONTROLLER_TOOLS_VERSION ?= v0.16.4

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate CRDs and RBAC into config/.
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role paths="./..."
	"$(CONTROLLER_GEN)" crd paths="./api/..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate DeepCopy methods.
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt" paths="./api/..."

.PHONY: fmt
fmt: ## Run go fmt.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: test
test: fmt vet ## Run unit tests.
	go test ./... -coverprofile cover.out

##@ Build

.PHONY: build
build: fmt vet ## Build the provisioner binary.
	go build -o bin/registry-provisioner ./cmd

.PHONY: run
run: fmt vet ## Run the provisioner from your host.
	go run ./cmd

.PHONY: docker-build
docker-build: ## Build the container image ($(IMG)).
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push the container image ($(IMG)).
	$(CONTAINER_TOOL) push ${IMG}

##@ Deployment

.PHONY: install
install: ## Install the CRDs into the cluster (KUBECONFIG / --context as needed).
	kubectl apply -k config/crd

.PHONY: uninstall
uninstall: ## Remove the CRDs from the cluster.
	kubectl delete -k config/crd --ignore-not-found=true

.PHONY: deploy
deploy: ## Deploy the operator (namespace, RBAC, CRDs, manager). Override IMG in config/default.
	kubectl apply -k config/default

.PHONY: undeploy
undeploy: ## Remove the operator from the cluster.
	kubectl delete -k config/default --ignore-not-found=true

##@ Tools

$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

.PHONY: controller-gen
controller-gen: $(LOCALBIN) ## Install controller-gen locally if missing.
	@test -s "$(CONTROLLER_GEN)" || GOBIN="$(LOCALBIN)" go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_TOOLS_VERSION)
