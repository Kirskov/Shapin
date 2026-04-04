package scanner

// Config holds the configuration for the scanner.
type Config struct {
	Path        string
	DryRun      bool
	GitHubToken string
	GitLabToken string
	GitLabHost  string
	PinActions  bool     // pin GitHub Actions `uses:` refs
	PinImages   bool     // pin Docker `image:` refs
	Exclude     []string // glob patterns of relative paths to skip
	Output      string   // write output to this file path instead of stdout (optional)
	Format      string   // output format: "text" (default), "json", or "sarif"
}
