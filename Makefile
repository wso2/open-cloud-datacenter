.DEFAULT_GOAL := help

DOCKER ?= docker
RUNNER_IMAGE ?=
RUNNER_IMAGE_TAG ?= 0.1.0
RUNNER_PLATFORM ?= linux/amd64
RUNNER_IMAGE_REF = $(RUNNER_IMAGE):$(RUNNER_IMAGE_TAG)
RUNNER_DOCKERFILE := pipeline/images/terraform-runner/Dockerfile

.PHONY: help check-runner-image terraform-runner-build terraform-runner-push \
	terraform-runner-publish terraform-runner-verify

help:
	@echo "Terraform runner targets:"
	@echo "  terraform-runner-build    Build the runner image locally"
	@echo "  terraform-runner-push     Push an already-built runner image"
	@echo "  terraform-runner-publish  Build and push the runner image"
	@echo "  terraform-runner-verify   Verify tools in the local runner image"
	@echo
	@echo "Required variable:"
	@echo "  RUNNER_IMAGE=<registry>/<namespace>/harvester-testsuite-terraform-runner"
	@echo
	@echo "Optional variables:"
	@echo "  RUNNER_IMAGE_TAG=$(RUNNER_IMAGE_TAG)"
	@echo "  RUNNER_PLATFORM=$(RUNNER_PLATFORM)"
	@echo "  DOCKER=$(DOCKER)"

check-runner-image:
	@if [ -z "$(strip $(RUNNER_IMAGE))" ]; then \
		echo "RUNNER_IMAGE is required" >&2; \
		echo "example: make terraform-runner-publish RUNNER_IMAGE=registry.example.com/team/harvester-testsuite-terraform-runner" >&2; \
		exit 2; \
	fi

terraform-runner-build: check-runner-image
	$(DOCKER) build \
		--platform "$(RUNNER_PLATFORM)" \
		--file "$(RUNNER_DOCKERFILE)" \
		--tag "$(RUNNER_IMAGE_REF)" \
		.

terraform-runner-push: check-runner-image
	$(DOCKER) push "$(RUNNER_IMAGE_REF)"

terraform-runner-publish: terraform-runner-build
	$(DOCKER) push "$(RUNNER_IMAGE_REF)"

terraform-runner-verify: check-runner-image
	$(DOCKER) run --rm --platform "$(RUNNER_PLATFORM)" \
		"$(RUNNER_IMAGE_REF)" terraform version
	$(DOCKER) run --rm --platform "$(RUNNER_PLATFORM)" \
		"$(RUNNER_IMAGE_REF)" git --version
	$(DOCKER) run --rm --platform "$(RUNNER_PLATFORM)" \
		"$(RUNNER_IMAGE_REF)" jq --version
