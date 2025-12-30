package provider

// Version is the version of the provider.
// This value is set at build time via ldflags, or defaults to "dev" for local development.
// For automated releases, this can be updated via:
//   - Git tags (via Makefile: `VERSION=$(git describe --tags --always --dirty)`)
//   - CI/CD environment variables
//   - GoReleaser (automatically sets via ldflags)
var Version = "dev"

// GetVersion returns the provider version.
// This follows the Terraform Plugin Framework pattern where the version
// is passed to the provider and exposed via Metadata.
func GetVersion() string {
	return Version
}
