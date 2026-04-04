package scanner

// Provider is the extension point for CI/CD file formats.
// Implement this interface to add support for a new provider.
type Provider interface {
	// Name returns a human-readable label used in log output.
	Name() string

	// IsMatch reports whether the given slash-separated path relative to the
	// scan root belongs to this provider.
	IsMatch(relPath string) bool

	// Resolve rewrites content, replacing floating tags with immutable SHAs.
	// pinActions controls action/component pinning; pinImages controls Docker
	// image pinning. Returns the (possibly unchanged) content and any fatal error.
	Resolve(content string, pinActions, pinImages bool) (string, error)
}
