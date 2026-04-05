package scanner

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"pintosha/provider"
	"pintosha/providers"
)

const (
	FormatText  = "text"
	FormatJSON  = "json"
	FormatSARIF = "sarif"

	filePerms = 0644

	skipNodeModules = "node_modules"
	skipGit         = ".git"
	skipVendor      = "vendor"
	skipDist        = "dist"
)

// Run is the main entrypoint: scans the project at cfg.Path and pins all refs.
func Run(cfg Config) error {
	out, closeOut, err := openOutput(cfg.Output)
	if err != nil {
		return err
	}
	defer closeOut()

	providerList := []provider.Provider{
		providers.NewGitHubResolver(cfg.GitHubToken),
		providers.NewGitLabResolver(cfg.GitLabHost, cfg.GitLabToken),
		providers.NewForgejoResolver(cfg.ForgejoHost, cfg.ForgejoToken),
		providers.NewCircleCIResolver(""),
		providers.NewBitbucketResolver(),
	}

	files, err := findWorkflowFiles(cfg.Path, providerList, cfg.Exclude)
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
			fc, err := processFile(f, cfg.Path, providerList, processOpts{
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
	noop := func() { /* no file to close */ }
	if path == "" {
		return os.Stdout, noop, nil
	}
	f, err := os.Create(path) // #nosec G304 — path is user-supplied --output flag, intentional
	if err != nil {
		return nil, noop, fmt.Errorf("opening output file: %w", err)
	}
	return f, func() {
		if err := f.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warn: closing output file: %v\n", err)
		}
	}, nil
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
			if name == skipNodeModules || name == skipGit || name == skipVendor || name == skipDist {
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

// matchProvider returns the first provider that matches the file's relative path.
func matchProvider(path, root string, providers []provider.Provider) (provider.Provider, error) {
	rel, _ := filepath.Rel(root, path)
	slashRel := filepath.ToSlash(rel)
	for _, p := range providers {
		if p.IsMatch(slashRel) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no provider matched %s", path)
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
	if err := assertWithinRoot(path, root); err != nil {
		return nil, err
	}

	matched, err := matchProvider(path, root, providers)
	if err != nil {
		return nil, err
	}

	raw, err := os.ReadFile(path) // #nosec G304 — path validated by assertWithinRoot
	if err != nil {
		return nil, err
	}
	original := string(raw)

	updated, err := matched.Resolve(original, opts.pinActions, opts.pinImages)
	if err != nil {
		return nil, err
	}

	if updated == original {
		return nil, nil
	}

	fc := collectChanges(path, original, updated)

	if opts.format == FormatText {
		printDiff(opts.out, path, original, updated)
	}

	if !opts.dryRun {
		if err := os.WriteFile(path, []byte(updated), filePerms); err != nil { // #nosec G703,G306 — path validated by assertWithinRoot; filePerms is 0644
			return nil, err
		}
		if opts.format == FormatText {
			fmt.Fprintf(opts.out, "  updated %s\n", path)
		}
	}

	return &fc, nil
}

// printDiff prints a colored unified-style diff of the changes.
func printDiff(out io.Writer, path, original, updated string) {
	reset := providers.Ansi(providers.AnsiReset)
	fmt.Fprintf(out, "\n%s%s--- %s%s\n", providers.Ansi(providers.AnsiBold), providers.Ansi(providers.AnsiCyan), path, reset)
	fmt.Fprintf(out, "%s%s+++ %s (pinned)%s\n", providers.Ansi(providers.AnsiBold), providers.Ansi(providers.AnsiCyan), path, reset)
	diffLines(original, updated, func(o, u string) {
		if o != "" {
			fmt.Fprintf(out, "%s-%s%s\n", providers.Ansi(providers.AnsiRed), o, reset)
		}
		if u != "" {
			fmt.Fprintf(out, "%s+%s%s\n", providers.Ansi(providers.AnsiGreen), u, reset)
		}
	})
}
