package main

import (
	"flag"
	"fmt"
	"os"

	"pintosha/scanner"
)

// Version, Commit and Date are set at build time via ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

func main() {
	path := flag.String("path", ".", "path to the project to scan")
	dryRun := flag.Bool("dry-run", false, "show changes without writing files")
	githubToken := flag.String("github-token", os.Getenv("GITHUB_TOKEN"), "GitHub API token")
	gitlabToken := flag.String("gitlab-token", os.Getenv("GITLAB_TOKEN"), "GitLab API token")
	gitlabHost := flag.String("gitlab-host", "https://gitlab.com", "GitLab host URL")
	pinActions := flag.Bool("pin-actions", true, "pin GitHub Actions uses: refs to SHAs")
	pinImages := flag.Bool("pin-images", true, "pin Docker image: tags to digests")
	version := flag.Bool("version", false, "print version and exit")
	flag.BoolVar(version, "v", false, "print version and exit")
	flag.Parse()

	if *version {
		fmt.Printf("%s (commit: %s, date: %s)\n", Version, Commit, Date)
		return
	}

	cfg := scanner.Config{
		Path:        *path,
		DryRun:      *dryRun,
		GitHubToken: *githubToken,
		GitLabToken: *gitlabToken,
		GitLabHost:  *gitlabHost,
		PinActions:  *pinActions,
		PinImages:   *pinImages,
	}

	if err := scanner.Run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
