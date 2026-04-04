package scanner

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"pintosha/provider"
)

const (
	FormatText = "text"
	FormatJSON = "json"
	FormatSARIF = "sarif"
)

// Run is the main entrypoint: scans the project at cfg.Path and pins all refs.
func Run(cfg Config) error {
	out, closeOut, err := openOutput(cfg.Output)
	if err != nil {
		return err
	}
	defer closeOut()

	providers := []provider.Provider{
		newGitHubResolver(cfg.GitHubToken),
		newGitLabResolver(cfg.GitLabHost, cfg.GitLabToken),
		newCircleCIResolver(""),
		newBitbucketResolver(),
	}

	files, err := findWorkflowFiles(cfg.Path, providers, cfg.Exclude)
	if err != nil {
		return fmt.Errorf("scanning path: %w", err)
	}

	if len(files) == 0 {
		fmt.Fprintln(out, "No workflow files found.")
		return nil
	}

	format := cfg.Format
	if format == "" {
		format = FormatText
	}

	const workers = 8
	var (
		anyChanged atomic.Bool
		wg         sync.WaitGroup
		sem        = make(chan struct{}, workers)
		mu         sync.Mutex
		changes    []FileChange
	)

	for _, file := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(f string) {
			defer wg.Done()
			defer func() { <-sem }()
			fc, err := processFile(f, cfg.Path, providers, processOpts{
					dryRun:     cfg.DryRun,
					pinActions: cfg.PinActions,
					pinImages:  cfg.PinImages,
					format:     format,
					out:        out,
				})
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: %s: %v\n", f, err)
				return
			}
			if fc != nil {
				anyChanged.Store(true)
				mu.Lock()
				changes = append(changes, *fc)
				mu.Unlock()
			}
		}(file)
	}
	wg.Wait()

	switch format {
	case FormatJSON:
		return renderJSON(out, changes)
	case FormatSARIF:
		return renderSARIF(out, changes)
	}

	if cfg.DryRun && anyChanged.Load() {
		fmt.Fprintln(out, "\n(dry-run) No files were modified.")
	} else if !anyChanged.Load() {
		fmt.Fprintln(out, "All refs already pinned — nothing to do.")
	}

	return nil
}

// openOutput returns a writer for cfg.Output (a file path) or stdout,
// plus a cleanup function to close the file if one was opened.
func openOutput(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stdout, func() { /* nothing to close */ }, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, func() { /* nothing to close */ }, fmt.Errorf("opening output file: %w", err)
	}
	return f, func() { f.Close() }, nil
}

// findWorkflowFiles returns all CI files matching any registered provider,
// skipping any paths that match an exclude glob pattern.
func findWorkflowFiles(root string, providers []provider.Provider, exclude []string) ([]string, error) {
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

		if isExcluded(slashRel, exclude) {
			return nil
		}

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

// isExcluded returns true if the relative path matches any of the exclude globs.
func isExcluded(relPath string, patterns []string) bool {
	for _, pattern := range patterns {
		matched, err := filepath.Match(pattern, relPath)
		if err == nil && matched {
			return true
		}
		// Also match against just the filename for simple patterns like "*.yml"
		matched, err = filepath.Match(pattern, filepath.Base(relPath))
		if err == nil && matched {
			return true
		}
	}
	return false
}

// processOpts groups the options passed to processFile.
type processOpts struct {
	dryRun     bool
	pinActions bool
	pinImages  bool
	format     string
	out        io.Writer
}

// processFile resolves refs in a single file and writes it (or prints a diff in dry-run mode).
// Returns a non-nil *FileChange if anything changed, nil if content was already pinned.
func processFile(path, root string, providers []provider.Provider, opts processOpts) (*FileChange, error) {
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
		return nil, fmt.Errorf("no provider matched %s", path)
	}

	original, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	updated, err := matched.Resolve(string(original), opts.pinActions, opts.pinImages)
	if err != nil {
		return nil, err
	}

	if updated == string(original) {
		return nil, nil
	}

	fc := collectChanges(path, string(original), updated)

	if opts.format == FormatText {
		printDiff(opts.out, path, string(original), updated)
	}

	if !opts.dryRun {
		if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
			return nil, err
		}
		if opts.format == FormatText {
			fmt.Fprintf(opts.out, "  updated %s\n", path)
		}
	}

	return &fc, nil
}

// isTTY reports whether stdout is an interactive terminal.
func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// color returns the ANSI escape code when stdout is a TTY, otherwise "".
func color(code string) string {
	if isTTY() {
		return code
	}
	return ""
}

var (
	colorReset  = color("\033[0m")
	colorRed    = color("\033[31m")
	colorGreen  = color("\033[32m")
	colorYellow = color("\033[33m")
	colorCyan   = color("\033[36m")
	colorBold   = color("\033[1m")
)

// printDiff prints a colored unified-style diff of the changes.
func printDiff(out io.Writer, path, original, updated string) {
	fmt.Fprintf(out, "\n%s%s--- %s%s\n", colorBold, colorCyan, path, colorReset)
	fmt.Fprintf(out, "%s%s+++ %s (pinned)%s\n", colorBold, colorCyan, path, colorReset)

	origLines := strings.Split(original, "\n")
	updLines := strings.Split(updated, "\n")

	maxLen := max(len(origLines), len(updLines))
	for i := range maxLen {
		var o, u string
		if i < len(origLines) {
			o = origLines[i]
		}
		if i < len(updLines) {
			u = updLines[i]
		}
		if o != u {
			if i < len(origLines) {
				fmt.Fprintf(out, "%s-%s%s\n", colorRed, o, colorReset)
			}
			if i < len(updLines) {
				fmt.Fprintf(out, "%s+%s%s\n", colorGreen, u, colorReset)
			}
		}
	}
}
