package scanner

// Config holds the configuration for the scanner.
type Config struct {
	Path        string
	DryRun      bool
	GitHubToken string
	GitLabToken string
	GitLabHost  string
	PinActions  bool // pin GitHub Actions `uses:` refs
	PinImages   bool // pin Docker `image:` refs
}
