package scanner

import (
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

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

	bearerPrefix = "Bearer "
	maxRetries   = 3
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
		colorBold, colorYellow, ref, tag, kind, colorReset, pinnedSHA, currentSHA)
}

// actionRepoPath extracts "owner/repo" from an action path like "owner/repo/subdir".
func actionRepoPath(action string) string {
	return strings.Join(strings.SplitN(action, "/", 3)[:2], "/")
}

func mustCompile(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
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

