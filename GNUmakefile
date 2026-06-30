OS   = $(shell go env GOOS)
ARCH = $(shell go env GOARCH)

BINARY      = terraform-provider-dcapi
INSTALL_DIR = $(HOME)/.terraform.d/plugins/registry.terraform.io/wso2/dcapi/0.1.0/$(OS)_$(ARCH)

build:
	go build -o $(BINARY) .

install: build
	mkdir -p $(INSTALL_DIR)
	cp $(BINARY) $(INSTALL_DIR)/$(BINARY)

clean:
	rm -f $(BINARY)

.PHONY: build install clean
