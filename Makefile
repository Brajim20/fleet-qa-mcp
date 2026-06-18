.PHONY: qa-mcp qa-setup qa-auth tidy clean

# Build the MCP server binary.
qa-mcp: tidy
	go build -o build/fleet-qa-mcp ./cmd/fleet-qa-mcp

# One-time: resolve deps + download the Playwright Chromium driver.
qa-setup: tidy
	go run ./cmd/fleet-qa-mcp --install-browsers

# Log in and write a reusable browser session (storageState), keyed per-host.
# Reads instance URL from ~/.fleet/config (or FLEET_URL). Prompts for creds
# if no token is available.
qa-auth: qa-mcp
	./build/fleet-qa-mcp --auth

tidy:
	go mod tidy

clean:
	rm -rf build .auth .fleet-src
