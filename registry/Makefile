# Registry operator — provisions per-tenant Harbor registries via CRDs.
# Layout follows the KeyVault operator (crds/keyvault) conventions.

IMG ?= controller:latest
CONTAINER_TOOL ?= docker

# Tooling
LOCALBIN ?= $(shell pwd)/bin
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen

## Tool Versions — kept in sync with the keyvault/database operators' Makefiles.
KUSTOMIZE_VERSION ?= v5.8.1
CONTROLLER_TOOLS_VERSION ?= v0.20.1

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

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: kustomize ## Install the CRDs into the cluster (KUBECONFIG / --context as needed).
	"$(KUSTOMIZE)" build config/crd | "$(KUBECTL)" apply -f -

.PHONY: uninstall
uninstall: kustomize ## Remove the CRDs from the cluster.
	"$(KUSTOMIZE)" build config/crd | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: kustomize ## Deploy the operator (namespace, RBAC, CRDs, manager) with generic placeholder config. Override IMG as needed.
	cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG}
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -

.PHONY: undeploy
undeploy: kustomize ## Remove the operator from the cluster.
	"$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy-local
deploy-local: kustomize ## Deploy using config/local — your real BASE_DOMAIN/STORAGE_CLASS (see config/local/manager_local_patch.yaml.example).
	@test -f config/local/manager_local_patch.yaml || { \
		echo "config/local/manager_local_patch.yaml not found."; \
		echo "Copy config/local/manager_local_patch.yaml.example to config/local/manager_local_patch.yaml and fill in your real values first."; \
		exit 1; \
	}
	"$(KUSTOMIZE)" build config/local | "$(KUBECTL)" apply -f -

.PHONY: undeploy-local
undeploy-local: kustomize ## Remove the operator deployed via deploy-local.
	"$(KUSTOMIZE)" build config/local | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -

##@ Tools

$(LOCALBIN):
	mkdir -p "$(LOCALBIN)"

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

# go-install-tool will 'go install' any package with custom target and name of
# binary, if it doesn't exist. Matches keyvault/database's Makefile exactly.
# $1 - target path with name of binary
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f "$(1)-$(3)" ] && [ "$$(readlink -- "$(1)" 2>/dev/null)" = "$(1)-$(3)" ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
rm -f "$(1)" ;\
GOBIN="$(LOCALBIN)" go install $${package} ;\
mv "$(LOCALBIN)/$$(basename "$(1)")" "$(1)-$(3)" ;\
} ;\
ln -sf "$$(realpath "$(1)-$(3)")" "$(1)"
endef
