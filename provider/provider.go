// Package provider defines the extension point for CI/CD file formats.
// Implement the Provider interface to add support for a new provider
// without modifying any existing code.
package provider

// Provider is the interface that every CI/CD provider must implement.
type Provider interface {
	// Name returns a human-readable label used in log output (e.g. "GitHub Actions").
	Name() string

	// IsMatch reports whether the given slash-separated path relative to the
	// scan root belongs to this provider.
	IsMatch(relPath string) bool

	// Resolve rewrites content, replacing floating tags with immutable SHAs.
	// pinActions controls action/component pinning; pinImages controls Docker
	// image pinning. Returns the (possibly unchanged) content and any fatal error.
	Resolve(content string, pinActions, pinImages bool) (string, error)
}
