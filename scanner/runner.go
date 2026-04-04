package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pintosha/provider"
)

// Run is the main entrypoint: scans the project at cfg.Path and pins all refs.
func Run(cfg Config) error {
	providers := []provider.Provider{
		newGitHubResolver(cfg.GitHubToken),
		newGitLabResolver(cfg.GitLabHost, cfg.GitLabToken),
	}

	files, err := findWorkflowFiles(cfg.Path, providers)
	if err != nil {
		return fmt.Errorf("scanning path: %w", err)
	}

	if len(files) == 0 {
		fmt.Println("No workflow files found.")
		return nil
	}

	anyChanged := false
	for _, file := range files {
		changed, err := processFile(file, cfg.Path, providers, cfg.DryRun, cfg.PinActions, cfg.PinImages)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warn: %s: %v\n", file, err)
			continue
		}
		if changed {
			anyChanged = true
		}
	}

	if cfg.DryRun && anyChanged {
		fmt.Println("\n(dry-run) No files were modified.")
	} else if !anyChanged {
		fmt.Println("All refs already pinned — nothing to do.")
	}

	return nil
}

// findWorkflowFiles returns all CI files matching any registered provider.
func findWorkflowFiles(root string, providers []provider.Provider) ([]string, error) {
	var files []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable dirs
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || name == ".git" || name == "vendor" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}

		rel, _ := filepath.Rel(root, path)
		slashRel := filepath.ToSlash(rel)

		for _, p := range providers {
			if p.IsMatch(slashRel) {
				files = append(files, path)
				break
			}
		}

		return nil
	})

	return files, err
}

// processFile resolves refs in a single file and writes it (or prints a diff in dry-run mode).
func processFile(path, root string, providers []provider.Provider, dryRun, pinActions, pinImages bool) (bool, error) {
	rel, _ := filepath.Rel(root, path)
	slashRel := filepath.ToSlash(rel)

	var matched provider.Provider
	for _, p := range providers {
		if p.IsMatch(slashRel) {
			matched = p
			break
		}
	}
	if matched == nil {
		return false, fmt.Errorf("no provider matched %s", path)
	}

	original, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	updated, err := matched.Resolve(string(original), pinActions, pinImages)
	if err != nil {
		return false, err
	}

	if updated == string(original) {
		return false, nil
	}

	printDiff(path, string(original), updated)

	if !dryRun {
		if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
			return false, err
		}
		fmt.Printf("  updated %s\n", path)
	}

	return true, nil
}

const (
	colorReset = "\033[0m"
	colorRed   = "\033[31m"
	colorGreen = "\033[32m"
	colorCyan  = "\033[36m"
	colorBold  = "\033[1m"
)

// printDiff prints a colored unified-style diff of the changes.
func printDiff(path, original, updated string) {
	fmt.Printf("\n%s%s--- %s%s\n", colorBold, colorCyan, path, colorReset)
	fmt.Printf("%s%s+++ %s (pinned)%s\n", colorBold, colorCyan, path, colorReset)

	origLines := strings.Split(original, "\n")
	updLines := strings.Split(updated, "\n")

	maxLen := len(origLines)
	if len(updLines) > maxLen {
		maxLen = len(updLines)
	}
	for i := 0; i < maxLen; i++ {
		var o, u string
		if i < len(origLines) {
			o = origLines[i]
		}
		if i < len(updLines) {
			u = updLines[i]
		}
		if o != u {
			if i < len(origLines) {
				fmt.Printf("%s-%s%s\n", colorRed, o, colorReset)
			}
			if i < len(updLines) {
				fmt.Printf("%s+%s%s\n", colorGreen, u, colorReset)
			}
		}
	}
}
