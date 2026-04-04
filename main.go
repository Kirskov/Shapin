package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"pintosha/scanner"
)

// Version, Commit and Date are set at build time via ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func main() {
	configPath := flag.String("config", "", "path to config file (default: .digestify.json)")
	path := flag.String("path", ".", "path to the project to scan")
	dryRun := flag.Bool("dry-run", true, "show changes without writing files")
	githubToken := flag.String("github-token", os.Getenv("GITHUB_TOKEN"), "GitHub API token")
	gitlabToken := flag.String("gitlab-token", os.Getenv("GITLAB_TOKEN"), "GitLab API token")
	gitlabHost := flag.String("gitlab-host", "https://gitlab.com", "GitLab host URL")
	pinActions := flag.Bool("pin-actions", true, "pin GitHub Actions uses: refs to SHAs")
	pinImages := flag.Bool("pin-images", true, "pin Docker image: tags to digests")
	exclude := flag.String("exclude", "", "comma-separated glob patterns to exclude (e.g. '.github/workflows/skip.yml')")
	output := flag.String("output", "", "write output to file instead of stdout")
	format := flag.String("format", "text", "output format: text, json, sarif")
	version := flag.Bool("version", false, "print version and exit")
	flag.BoolVar(version, "v", false, "print version and exit")
	flag.Parse()

	if *version {
		fmt.Printf("%s (commit: %s, date: %s)\n", Version, Commit, Date)
		return
	}

	// Track which flags were explicitly set on the CLI so config file
	// values don't override them.
	explicitly := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) { explicitly[f.Name] = true })

	var excludePatterns []string
	if *exclude != "" {
		for p := range strings.SplitSeq(*exclude, ",") {
			if t := strings.TrimSpace(p); t != "" {
				excludePatterns = append(excludePatterns, t)
			}
		}
	}

	cfg := scanner.Config{
		Path:        *path,
		DryRun:      *dryRun,
		GitHubToken: *githubToken,
		GitLabToken: *gitlabToken,
		GitLabHost:  *gitlabHost,
		PinActions:  *pinActions,
		PinImages:   *pinImages,
		Exclude:     excludePatterns,
		Output:      *output,
		Format:      *format,
	}

	cfgFile, err := scanner.LoadConfigFile(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	cfgFile.ApplyTo(&cfg, explicitly)

	if err := scanner.Run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
