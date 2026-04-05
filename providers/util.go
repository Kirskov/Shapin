package providers

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	AnsiReset  = "\033[0m"
	AnsiRed    = "\033[31m"
	AnsiGreen  = "\033[32m"
	AnsiYellow = "\033[33m"
	AnsiCyan   = "\033[36m"
	AnsiBold   = "\033[1m"
)

// IsTTY reports whether stdout is an interactive terminal.
func IsTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// Ansi returns the ANSI escape code when stdout is a TTY, otherwise "".
func Ansi(code string) string {
	if IsTTY() {
		return code
	}
	return ""
}

// Regex pattern constants — centralised so no pattern is duplicated across files.
const (
	patternSHA        = `^[0-9a-f]{40}$`
	patternDockerImage = `(image:\s+['"]?)([a-zA-Z0-9_.\-/]+):([a-zA-Z0-9_.\-]+)(['"]?)`
	patternDockerPinned = `image:\s+['"]?([a-zA-Z0-9_.\-/]+)@(sha256:[0-9a-f]+)['"]?\s+#\s+(\S+)`
	patternGHAction   = `(uses:\s+)([a-zA-Z0-9_.-]+/[a-zA-Z0-9_./%-]+)@([^\s#]+)`
	patternGHPinned   = `uses:\s+([a-zA-Z0-9_.-]+/[a-zA-Z0-9_./%-]+)@([0-9a-f]{40})\s+#\s+(\S+)`
	patternGLComponent = `(component:\s+)([a-zA-Z0-9_.\-/]+)@([^\s#]+)`
	patternGLPinned   = `component:\s+([a-zA-Z0-9_.\-/]+)@([0-9a-f]{40})\s+#\s+(\S+)`
	patternGLInputTag = `(?m)^(\s+[A-Z0-9_]*TAG[A-Z0-9_]*:\s+['"]?)([a-zA-Z0-9_.\-/]+):([a-zA-Z0-9_.\-]+)(['"]?\s*)$`

	bearerPrefix  = "Bearer "
	maxRetries    = 3
	httpTimeout   = 30 * time.Second
)

var shaRegex = regexp.MustCompile(patternSHA)

// isSHA returns true if the string looks like a full git SHA or docker digest.
func isSHA(s string) bool {
	return shaRegex.MatchString(s) || strings.HasPrefix(s, "sha256:")
}

// syncCache is a thread-safe string→string cache.
type syncCache struct {
	mu    sync.Mutex
	items map[string]string
}

func newSyncCache() syncCache {
	return syncCache{items: make(map[string]string)}
}

func (c *syncCache) getOrSet(key string, fetch func() (string, error)) (string, error) {
	c.mu.Lock()
	v, ok := c.items[key]
	c.mu.Unlock()
	if ok {
		return v, nil
	}
	v, err := fetch()
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	// Check again under lock in case another goroutine raced us.
	if existing, ok := c.items[key]; ok {
		c.mu.Unlock()
		return existing, nil
	}
	c.items[key] = v
	c.mu.Unlock()
	return v, nil
}

// replaceMatches calls fn for each non-overlapping match of re in content.
// fn receives the submatches slice; if it returns ("", false) the original match is kept.
func replaceMatches(re *regexp.Regexp, content string, fn func(parts []string) (string, bool)) string {
	return re.ReplaceAllStringFunc(content, func(match string) string {
		parts := re.FindStringSubmatch(match)
		if len(parts) == 0 {
			return match
		}
		if replacement, ok := fn(parts); ok {
			return replacement
		}
		return match
	})
}

// warnDrift prints a warning when a pinned ref's SHA no longer matches.
func warnDrift(kind, ref, tag, pinnedSHA, currentSHA string) {
	fmt.Printf("%s%sWARNING: %s@%s has drifted — %s was mutated!%s\n  pinned:  %s\n  current: %s\n  → update this ref manually\n",
		Ansi(AnsiBold), Ansi(AnsiYellow), ref, tag, kind, Ansi(AnsiReset), pinnedSHA, currentSHA)
}

// actionPinner pins `uses: owner/repo@tag` refs to their SHAs using a shared cache.
// It is used by both the GitHub and Forgejo resolvers which share the same regex and
// output format, differing only in their fetch function and error prefix.
type actionPinner struct {
	name    string // used in error messages, e.g. "GitHub", "Forgejo"
	cache   syncCache
	resolve func(repo, ref string) (string, error)
}

func (ap *actionPinner) pin(content string) (string, error) {
	var resolveErr error
	result := replaceMatches(githubActionRegex, content, func(parts []string) (string, bool) {
		if resolveErr != nil {
			return "", false
		}
		prefix, action, ref := parts[1], parts[2], parts[3]
		if isSHA(ref) {
			return "", false
		}
		repoPath := actionRepoPath(action)
		sha, err := ap.cache.getOrSet(repoPath+"@"+ref, func() (string, error) {
			return ap.resolve(repoPath, ref)
		})
		if err != nil {
			resolveErr = fmt.Errorf("%s: %s@%s: %w", ap.name, repoPath, ref, err)
			return "", false
		}
		return fmt.Sprintf("%s%s@%s # %s", prefix, action, sha, ref), true
	})
	return result, resolveErr
}

// driftChecker checks already-pinned refs for SHA drift.
// Use checkAll to scan content for all matches of pinnedRegex and warn for any
// whose current SHA no longer matches the pinned one.
type driftChecker struct {
	// pinnedRegex must have 3 capture groups: (ref, pinnedSHA, tag)
	pinnedRegex *regexp.Regexp
	// kind is a human-readable label used in the warning message (e.g. "tag", "image")
	kind string
	// resolve fetches the current SHA for the given ref and tag
	resolve func(ref, tag string) (string, error)
	// repoPath optionally transforms the ref before passing it to resolve
	repoPath func(ref string) string
}

// checkAll scans content for pinned refs and warns about any that have drifted.
func (d *driftChecker) checkAll(content string) {
	for _, parts := range d.pinnedRegex.FindAllStringSubmatch(content, -1) {
		ref, pinnedSHA, tag := parts[1], parts[2], parts[3]
		lookupRef := ref
		if d.repoPath != nil {
			lookupRef = d.repoPath(ref)
		}
		currentSHA, err := d.resolve(lookupRef, tag)
		if err != nil {
			continue
		}
		if currentSHA != pinnedSHA {
			warnDrift(d.kind, ref, tag, pinnedSHA, currentSHA)
		}
	}
}

// actionRepoPath extracts "owner/repo" from an action path like "owner/repo/subdir".
func actionRepoPath(action string) string {
	return strings.Join(strings.SplitN(action, "/", 3)[:2], "/")
}

func mustCompile(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}

// newHTTPClient returns an http.Client with a sensible default timeout.
func newHTTPClient() *http.Client {
	return &http.Client{Timeout: httpTimeout}
}

func isYAML(name string) bool {
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

func slashDir(path string) string {
	return filepath.ToSlash(filepath.Dir(path))
}

func slashBase(path string) string {
	return filepath.Base(path)
}

// assertWithinRoot returns an error if path is not contained within root,
// preventing directory traversal attacks.
func assertWithinRoot(path, root string) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolving root: %w", err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}
	if !strings.HasPrefix(absPath, absRoot+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes root %q", path, root)
	}
	return nil
}

// doWithRetry executes the request, retrying on 429 (rate limited) or 503
// up to maxRetries times, honouring Retry-After and X-RateLimit-Reset headers.
func doWithRetry(client *http.Client, req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		return nil, fmt.Errorf("request URL is nil")
	}
	host := req.URL.Hostname()
	if req.URL.Scheme != "https" && host != "127.0.0.1" && host != "localhost" {
		return nil, fmt.Errorf("only HTTPS requests are allowed, got: %s", req.URL)
	}
	for attempt := range maxRetries + 1 {
		// Clone the request on retries so the body can be re-sent if needed.
		r := req
		if attempt > 0 {
			r = req.Clone(req.Context())
		}

		resp, err := client.Do(r) // #nosec G704 — URL scheme validated above (https only)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable {
			return resp, nil
		}

		if err := resp.Body.Close(); err != nil {
			return nil, fmt.Errorf("closing response body: %w", err)
		}

		if attempt == maxRetries {
			return nil, fmt.Errorf("rate limited after %d retries (HTTP %d)", maxRetries, resp.StatusCode)
		}

		delay := retryDelay(resp)
		time.Sleep(delay)
	}
	// unreachable
	return nil, fmt.Errorf("unexpected retry loop exit")
}

// retryDelay returns how long to wait before the next attempt.
// It reads Retry-After (seconds or HTTP-date) and X-RateLimit-Reset (unix timestamp).
func retryDelay(resp *http.Response) time.Duration {
	const fallback = 60 * time.Second

	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			return time.Duration(secs) * time.Second
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := time.Until(t); d > 0 {
				return d
			}
		}
	}

	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		if unix, err := strconv.ParseInt(v, 10, 64); err == nil {
			if d := time.Until(time.Unix(unix, 0)); d > 0 {
				return d
			}
		}
	}

	return fallback
}

