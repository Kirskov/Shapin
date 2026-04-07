package scanner

// Config holds the configuration for the scanner.
type Config struct {
	Path        string
	DryRun      bool
	GitHubToken string
	GitLabToken string
	GitLabHost    string
	ForgejoHost   string
	ForgejoToken  string
	PinActions  bool              // pin GitHub Actions `uses:` refs
	PinImages   bool              // pin Docker `image:` refs
	Exclude     []string          // glob patterns of relative paths to skip
	Output      string            // write output to this file path instead of stdout (optional)
	Format      string            // output format: "text" (default), "json", or "sarif"
	TagMappings map[string]string // maps input key names (e.g. NODE_TAG) to image names (e.g. node)
}
