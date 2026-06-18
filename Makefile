.PHONY: help qa-mcp qa-setup qa-auth test vet tidy clean

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-12s %s\n", $$1, $$2}'

qa-mcp: tidy ## Build the MCP/CLI binary
	go build -o build/fleet-qa-mcp ./cmd/fleet-qa-mcp

qa-setup: tidy ## One-time: deps + download Playwright Chromium
	go run ./cmd/fleet-qa-mcp --install-browsers

qa-auth: qa-mcp ## Write a reusable browser session from the admin token
	./build/fleet-qa-mcp --auth

test: ## Run unit tests
	go test ./...

vet: ## go vet
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf build .auth .fleet-src
