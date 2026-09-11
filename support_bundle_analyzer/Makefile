.PHONY: help load up down clean

HARV_LOGS_RELEASE_URL ?=

# Default target
help:
	@echo "Usage:"
	@echo "  make load BUNDLE=<path-to-zip>  - Load a locally built plugin and bundle into Loki"
	@echo "  make HARV_LOGS_RELEASE_URL=<archive-url> load BUNDLE=<path-to-zip>"
	@echo "  make up                         - Start the RCA stack"
	@echo "  make down                       - Stop the RCA stack"
	@echo "  make clean                      - Remove extracted logs"

load:
	@if [ -z "$(BUNDLE)" ]; then \
		echo "Error: BUNDLE path is required."; \
		echo "Usage: make load BUNDLE=path/to/support-bundle.zip"; \
		exit 1; \
	fi
	@if [ ! -d "dist" ] && [ -z "$(HARV_LOGS_RELEASE_URL)" ]; then \
		echo "Error: dist/ is missing and no plugin release URL was provided."; \
		echo "Follow grafana-plugin/CONTRIBUTOR.md to build the plugin locally."; \
		echo "Then copy grafana-plugin/dist to dist/ and rerun make load."; \
		exit 1; \
	fi
	@if [ -n "$(HARV_LOGS_RELEASE_URL)" ]; then \
		HARV_LOGS_RELEASE_URL="$(HARV_LOGS_RELEASE_URL)" bash scripts/download-plugin.sh; \
	fi
	bash scripts/load-logs.sh "$(BUNDLE)"

up:
	docker compose up -d

down:
	docker compose down -v

clean:
	rm -rf ./bundle-logs
	rm -rf ./dist
