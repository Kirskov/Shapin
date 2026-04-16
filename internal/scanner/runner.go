package scanner

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/Kirskov/Shapin/internal/contract"
	"github.com/Kirskov/Shapin/internal/providers"
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

	providerList := []contract.Provider{
		providers.NewGitHubResolver(cfg.GitHubToken),
		providers.NewGitLabResolver(cfg.GitLabHost, cfg.GitLabToken, cfg.TagMappings),
		providers.NewForgejoResolver(cfg.ForgejoHost, cfg.ForgejoToken),
		providers.NewCircleCIResolver(""),
		providers.NewBitbucketResolver(),
		providers.NewWoodpeckerResolver(),
		providers.NewDockerfileResolver(),
		providers.NewComposeResolver(),
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
		allWarnings []fileWarning
	)

	for _, file := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(filePath string) {
			defer wg.Done()
			defer func() { <-sem }()
			fileChange, warns, err := processFile(filePath, cfg.Path, providerList, processOpts{
				dryRun:     cfg.DryRun,
				pinActions: cfg.PinActions,
				pinImages:  cfg.PinImages,
				format:     format,
				out:        out,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "warn: %s: %v\n", filePath, err)
				return
			}
			mu.Lock()
			if fileChange != nil {
				anyChanged.Store(true)
				changes = append(changes, *fileChange)
			}
			for _, w := range warns {
				allWarnings = append(allWarnings, fileWarning{file: filePath, msg: w})
			}
			mu.Unlock()
		}(file)
	}
	wg.Wait()

	switch format {
	case FormatJSON:
		return renderJSON(out, changes)
	case FormatSARIF:
		return renderSARIF(out, changes, cfg.Version)
	}

	if cfg.DryRun && anyChanged.Load() {
		fmt.Fprintln(out, "\n(dry-run) No files were modified.")
	} else if !anyChanged.Load() {
		fmt.Fprintln(out, nothingToDoMessage(cfg.PinActions, cfg.PinImages))
	}

	if len(allWarnings) > 0 {
		fmt.Fprintln(os.Stderr, "\nWarnings:")
		for _, w := range allWarnings {
			fmt.Fprintf(os.Stderr, "  %s: %s\n", w.file, w.msg)
		}
	}

	return nil
}

type fileWarning struct {
	file string
	msg  string
}

func nothingToDoMessage(pinActions, pinImages bool) string {
	switch {
	case !pinActions && !pinImages:
		return "Nothing to do — both --pin-refs and --pin-images are disabled."
	case !pinImages:
		return "Nothing to do — image pinning is disabled (--pin-images=false)."
	case !pinActions:
		return "Nothing to do — ref pinning is disabled (--pin-refs=false)."
	default:
		return "Everything already pinned — nothing to do."
	}
}

// openOutput returns a writer for cfg.Output (a file path) or stdout,
// plus a cleanup function to close the file if one was opened.
func openOutput(path string) (io.Writer, func(), error) {
	noop := func() { /* no file to close */ }
	if path == "" {
		return os.Stdout, noop, nil
	}
	outputFile, err := os.Create(path) // #nosec G304 — path is user-supplied --output flag, intentional
	if err != nil {
		return nil, noop, fmt.Errorf("opening output file: %w", err)
	}
	return outputFile, func() {
		if err := outputFile.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "warn: closing output file: %v\n", err)
		}
	}, nil
}

// findWorkflowFiles returns all CI files matching any registered provider,
// skipping any paths that match an exclude glob pattern.
func findWorkflowFiles(root string, providers []contract.Provider, exclude []string) ([]string, error) {
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
	base := filepath.Base(relPath)
	for _, pattern := range patterns {
		if matchesGlob(pattern, relPath) || matchesGlob(pattern, base) {
			return true
		}
	}
	return false
}

// matchesGlob returns true if name matches pattern, printing a warning for malformed patterns.
func matchesGlob(pattern, name string) bool {
	matched, err := filepath.Match(pattern, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: invalid exclude pattern %q: %v\n", pattern, err)
		return false
	}
	return matched
}

// matchProvider returns the first provider that matches the file's relative path.
func matchProvider(path, root string, providers []contract.Provider) (contract.Provider, error) {
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
// Returns a non-nil *FileChange if anything changed, nil if content was already pinned,
// and any non-fatal warnings emitted during resolution.
func processFile(path, root string, providers []contract.Provider, opts processOpts) (*FileChange, []string, error) {
	if err := assertWithinRoot(path, root); err != nil {
		return nil, nil, err
	}

	matched, err := matchProvider(path, root, providers)
	if err != nil {
		return nil, nil, err
	}

	raw, err := os.ReadFile(path) // #nosec G304 — path validated by assertWithinRoot
	if err != nil {
		return nil, nil, err
	}
	original := string(raw)

	updated, warns, err := matched.Resolve(original, opts.pinActions, opts.pinImages)
	if err != nil {
		return nil, warns, err
	}

	if updated == original {
		return nil, warns, nil
	}

	fc := collectChanges(path, original, updated)

	if opts.format == FormatText {
		printDiff(opts.out, path, original, updated)
	}

	if !opts.dryRun {
		if err := os.WriteFile(path, []byte(updated), filePerms); err != nil { // #nosec G703,G306 — path validated by assertWithinRoot; filePerms is 0644
			return nil, warns, err
		}
		if opts.format == FormatText {
			fmt.Fprintf(opts.out, "  updated %s\n", path)
		}
	}

	return &fc, warns, nil
}

// printDiff prints a colored unified-style diff of the changes.
func printDiff(out io.Writer, path, original, updated string) {
	reset := providers.Ansi(providers.AnsiReset)
	fmt.Fprintf(out, "\n%s%s--- %s%s\n", providers.Ansi(providers.AnsiBold), providers.Ansi(providers.AnsiCyan), path, reset)
	fmt.Fprintf(out, "%s%s+++ %s (pinned)%s\n", providers.Ansi(providers.AnsiBold), providers.Ansi(providers.AnsiCyan), path, reset)
	diffLines(original, updated, func(_ int, o, u string) {
		if o != "" {
			fmt.Fprintf(out, "%s-%s%s\n", providers.Ansi(providers.AnsiRed), o, reset)
		}
		if u != "" {
			fmt.Fprintf(out, "%s+%s%s\n", providers.Ansi(providers.AnsiGreen), u, reset)
		}
	})
}
