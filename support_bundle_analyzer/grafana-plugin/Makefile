.PHONY: help load up down clean

# Default target
help:
	@echo "Usage:"
	@echo "  make load BUNDLE=<path-to-zip>  - Unzip bundle and load into Loki"
	@echo "  make up                         - Start the RCA stack"
	@echo "  make down                       - Stop the RCA stack"
	@echo "  make clean                      - Remove extracted logs"

load:
	@if [ -z "$(BUNDLE)" ]; then \
		echo "Error: BUNDLE path is required."; \
		echo "Usage: make load BUNDLE=path/to/support-bundle.zip"; \
		exit 1; \
	fi
	bash ../scripts/load-logs.sh "$(BUNDLE)"

up:
	docker compose up -d

down:
	docker compose down -v

clean:
	rm -rf ../bundle-logs
	rm -rf ./dist
