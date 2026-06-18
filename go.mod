module github.com/fleetdm/fleet-qa-mcp

go 1.23

// Run `go mod tidy` after cloning to pin exact versions. These are the
// libraries the scaffold uses; tidy will resolve the latest compatible tags.
require (
	github.com/mark3labs/mcp-go v0.27.0
	github.com/playwright-community/playwright-go v0.4702.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/joho/godotenv v1.5.1
	github.com/spf13/pflag v1.0.10
)

require (
	github.com/deckarep/golang-set/v2 v2.6.0 // indirect
	github.com/go-jose/go-jose/v3 v3.0.3 // indirect
	github.com/go-stack/stack v1.8.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	golang.org/x/exp v0.0.0-20240506185415-9bf2ced13842 // indirect
)
