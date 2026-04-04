package scanner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Run is the main entrypoint: scans the project at cfg.Path and pins all refs.
func Run(cfg Config) error {
	files, err := findWorkflowFiles(cfg.Path)
	if err != nil {
		return fmt.Errorf("scanning path: %w", err)
	}

	if len(files) == 0 {
		fmt.Println("No workflow files found.")
		return nil
	}

	gh := newGitHubResolver(cfg.GitHubToken)
	gl := newGitLabResolver(cfg.GitLabHost, cfg.GitLabToken)

	anyChanged := false
	for _, file := range files {
		changed, err := processFile(file, gh, gl, cfg.DryRun, cfg.PinActions, cfg.PinImages)
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

// findWorkflowFiles returns all GitHub Actions workflow files and GitLab CI files
// under the given root path.
func findWorkflowFiles(root string) ([]string, error) {
	var files []string

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable dirs
		}
		if d.IsDir() {
			// Skip common noise directories
			name := d.Name()
			if name == "node_modules" || name == ".git" || name == "vendor" || name == "dist" {
				return filepath.SkipDir
			}
			return nil
		}

		rel, _ := filepath.Rel(root, path)

		// GitHub Actions: .github/workflows/*.yml or *.yaml
		if isGitHubWorkflow(rel) {
			files = append(files, path)
			return nil
		}

		// GitLab CI: .gitlab-ci.yml, .gitlab-ci.yaml, or files included via include:
		if isGitLabCI(rel) {
			files = append(files, path)
			return nil
		}

		return nil
	})

	return files, err
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

func isGitHubWorkflow(rel string) bool {
	dir := filepath.ToSlash(filepath.Dir(rel))
	return (dir == ".github/workflows" || strings.HasPrefix(dir, ".github/workflows/")) &&
		isYAML(filepath.Base(rel))
}

func isGitLabCI(rel string) bool {
	dir := filepath.ToSlash(filepath.Dir(rel))
	name := filepath.Base(rel)

	// Root-level .gitlab-ci.yml or .gitlab-ci-*.yml
	if dir == "." && (name == ".gitlab-ci.yml" || name == ".gitlab-ci.yaml" ||
		strings.HasPrefix(name, ".gitlab-ci-") && isYAML(name)) {
		return true
	}

	// Any YAML file inside .gitlab/ or .gitlab/<subfolder>/
	if dir == ".gitlab" || strings.HasPrefix(dir, ".gitlab/") {
		return isYAML(name)
	}

	return false
}

// processFile resolves refs in a single file and writes it (or prints a diff in dry-run mode).
func processFile(path string, gh *githubResolver, gl *gitlabResolver, dryRun, pinActions, pinImages bool) (bool, error) {
	original, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	content := string(original)
	var updated string

	if isGitHubWorkflow(filepath.ToSlash(path)) {
		updated, err = gh.resolve(content, pinActions, pinImages)
	} else {
		updated, err = gl.resolve(content, pinActions, pinImages)
	}
	if err != nil {
		return false, err
	}

	if updated == content {
		return false, nil
	}

	printDiff(path, content, updated)

	if !dryRun {
		if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
			return false, err
		}
		fmt.Printf("  updated %s\n", path)
	}

	return true, nil
}


const (
	colorReset  = "\033[0m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorCyan   = "\033[36m"
	colorBold   = "\033[1m"
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
