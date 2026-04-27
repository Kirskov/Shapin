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

// gitlabDependencyProxyVars are the predefined GitLab CI variables that expand
// to a dependency proxy image prefix (e.g. "gitlab.example.com/group/dependency_proxy/containers").
// When used as `image: ${VAR}/alpine:3.20`, the actual image pulled is `alpine:3.20`
// from Docker Hub. Shapin strips these prefixes before resolving the image.
var gitlabDependencyProxyVars = []string{
	"${CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX}",
	"${CI_DEPENDENCY_PROXY_DIRECT_GROUP_IMAGE_PREFIX}",
	"$CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX",
	"$CI_DEPENDENCY_PROXY_DIRECT_GROUP_IMAGE_PREFIX",
}

// stripDependencyProxyPrefix removes a known GitLab dependency proxy variable
// prefix (e.g. "${CI_DEPENDENCY_PROXY_GROUP_IMAGE_PREFIX}/") from a single
// image reference string (not the whole file), returning the stripped value
// and whether a substitution occurred.
func stripDependencyProxyPrefix(imageRef string) (string, bool) {
	for _, v := range gitlabDependencyProxyVars {
		prefix := v + "/"
		if strings.HasPrefix(imageRef, prefix) {
			return imageRef[len(prefix):], true
		}
	}
	return imageRef, false
}

// Regex pattern constants — centralised so no pattern is duplicated across files.
const (
	patternSHA          = `^[0-9a-f]{40}$`
	patternDockerImage   = `(?m)^([^#\n]*image:\s+['"]?)(\$\{[A-Z0-9_]+\}/|\$[A-Z0-9_]+/)?([a-zA-Z0-9_.\-/]+):([a-zA-Z0-9_.\-]+)(['"]?)`
	patternDockerName    = `(?m)^([^#\n]*name:\s+['"]?)(\$\{[A-Z0-9_]+\}/|\$[A-Z0-9_]+/)?([a-zA-Z0-9_.\-/]+):([a-zA-Z0-9_.\-]+)(['"]?)`
	patternGLService     = `(?m)^([\t ]*-[\t ]+['"]?)()([a-zA-Z0-9_.\-/]+):([a-zA-Z0-9_.\-]+)(['"]?[\t ]*)$`
	patternDockerPinned = `image:\s+['"]?([a-zA-Z0-9_.\-/]+)@(sha256:[0-9a-f]+)['"]?\s+#\s+(\S+)`
	patternFromLine     = `(?m)^(FROM\s+)([a-zA-Z0-9_.\-/]+):([a-zA-Z0-9_.\-]+)([ \t][^\n]*\n|\n)`
	patternFromPinned   = `(?m)^#\s+([a-zA-Z0-9_.\-/]+):([a-zA-Z0-9_.\-]+)\nFROM\s+[a-zA-Z0-9_.\-/]+@(sha256:[0-9a-f]+)`
	patternGHAction     = `(uses:\s+)([a-zA-Z0-9_.-]+/[a-zA-Z0-9_./%-]+)@([^\s#]+)`
	patternGHPinned     = `uses:\s+([a-zA-Z0-9_.-]+/[a-zA-Z0-9_./%-]+)@([0-9a-f]{40})\s+#\s+(\S+)`
	patternGLComponent  = `(component:\s+)(\$?[a-zA-Z0-9_.\-/]+)@([^\s#]+)`
	patternGLPinned     = `component:\s+([a-zA-Z0-9_.\-/]+)@([0-9a-f]{40})\s+#\s+(\S+)`
	patternGLInputTag     = `(?m)^(\s+[A-Z0-9_]*TAG[A-Z0-9_]*:\s+['"]?)([a-zA-Z0-9_.\-/]+):([a-zA-Z0-9_.\-]+)(['"]?\s*)$`
	patternGLMappedVersion  = `(?m)^(\s*)([A-Z0-9_]+):\s+(['"]?)([A-Za-z0-9][A-Za-z0-9._\-]*)(['"]?)[^\S\n]*$`
	patternTFVarsImage      = `(?m)^(\s*\w+\s*=\s*")([a-zA-Z0-9_.\-/]+):([a-zA-Z0-9_.\-]+)(")`
	patternTFVarsPinned     = `(?m)^(\s*\w+\s*=\s*")([a-zA-Z0-9_.\-/]+)@(sha256:[0-9a-f]+)\s*#\s*[a-zA-Z0-9_.\-/]+:([a-zA-Z0-9_.\-]+)(")`

	bearerPrefix = "Bearer "
	maxRetries   = 3
	httpTimeout  = 30 * time.Second
)

var shaRegex = regexp.MustCompile(patternSHA)

// builtinStemMappings maps common CI variable stems to their Docker Hub image.
// A stem is the variable name with a _TAG / _VERSION / _DIGEST suffix stripped,
// e.g. TF_VERSION → stem "TF" → "hashicorp/terraform".
// User-supplied tag-mappings in .shapin.json extend or override these.
var builtinStemMappings = map[string]string{
	"TF":        "hashicorp/terraform",
	"TERRAFORM": "hashicorp/terraform",
	"NODE":      "node",
	"NODEJS":    "node",
	"TRIVY":     "aquasec/trivy",
	"JAVA":      "eclipse-temurin",
	"ALPINE":    "alpine",
	"PYTHON":    "python",
	"GO":        "golang",
	"GOLANG":    "golang",
	"RUBY":      "ruby",
	"RUST":      "rust",
	"DOTNET":    "mcr.microsoft.com/dotnet/sdk",
	"KUBECTL":   "bitnami/kubectl",
	"HELM":      "alpine/helm",
	"POSTGRES":  "postgres",
	"MYSQL":     "mysql",
	"REDIS":     "redis",
	"NGINX":     "nginx",
	"SONARQUBE": "sonarsource/sonar-scanner-cli",
	"SONAR":     "sonarsource/sonar-scanner-cli",
	"AWS_CLI":       "amazon/aws-cli",
	"AWSCLI":        "amazon/aws-cli",
	"CURL":          "curlimages/curl",
	"GIT_CLIFF":     "orhunp/git-cliff",
	"DOCKER":        "docker",
	"DIND":          "docker",
	"KANIKO":        "gcr.io/kaniko-project/executor",
	"GRADLE":        "gradle",
	"MAVEN":         "maven",
	"MVN":           "maven",
	"PHP":           "php",
	"ELASTICSEARCH": "elasticsearch",
	"ES":            "elasticsearch",
	"MONGO":         "mongo",
	"MONGODB":       "mongo",
	"RABBITMQ":      "rabbitmq",
	"GRYPE":         "anchore/grype",
	"SEMGREP":       "semgrep/semgrep",
	"COSIGN":        "cgr.dev/chainguard/cosign",
	"PACKER":        "hashicorp/packer",
	"VAULT":         "hashicorp/vault",
	"GOLANGCI":      "golangci/golangci-lint",
	"GOLANGCI_LINT": "golangci/golangci-lint",
	"OPENTOFU":      "ghcr.io/opentofu/opentofu",
	"TOFU":          "ghcr.io/opentofu/opentofu",
	"VALKEY":        "valkey/valkey",
	"GRAFANA":      "grafana/grafana",
	"PROMETHEUS":   "prom/prometheus",
	"ALERTMANAGER": "prom/alertmanager",
	"TRAEFIK":      "traefik",
	"CADDY":        "caddy",
	"TELEGRAF":     "telegraf",
	"BASH":         "bash",
	"SELENIUM":     "selenium/standalone-chrome",
	"SYFT":         "anchore/syft",
}

// versionMarkers are the tokens that may appear as a prefix or suffix
// (with underscore separator) in a variable name to indicate a version value.
// e.g. TF_VERSION, VERSION_TF, TF_TAG, TAG_TF → stem "TF"
var versionMarkers = []string{"VERSION", "TAG", "DIGEST"}

// extractStem strips a version marker prefix or suffix from a variable name
// and returns the upper-case stem, or "" if no marker is present.
func extractStem(key string) string {
	upper := strings.ToUpper(key)
	for _, marker := range versionMarkers {
		if strings.HasSuffix(upper, "_"+marker) {
			return upper[:len(upper)-len("_"+marker)]
		}
		if strings.HasPrefix(upper, marker+"_") {
			return upper[len(marker+"_"):]
		}
	}
	return ""
}

// toDigestKey renames a version marker in a variable name to DIGEST,
// matching the case of the original marker.
// e.g. TF_VERSION → TF_DIGEST, version_tf → digest_tf, TF_TAG → TF_DIGEST
func toDigestKey(key string) string {
	upper := strings.ToUpper(key)
	for _, marker := range versionMarkers {
		if marker == "DIGEST" {
			continue
		}
		if strings.HasSuffix(upper, "_"+marker) {
			original := key[len(key)-len(marker):]
			replacement := matchCase("DIGEST", original)
			return key[:len(key)-len(marker)] + replacement
		}
		if strings.HasPrefix(upper, marker+"_") {
			original := key[:len(marker)]
			replacement := matchCase("DIGEST", original)
			return replacement + key[len(marker):]
		}
	}
	return key
}

// matchCase returns s in lowercase if ref is all lowercase, otherwise uppercase.
func matchCase(s, ref string) string {
	if ref == strings.ToLower(ref) {
		return strings.ToLower(s)
	}
	return s
}

// isSHA returns true if the string looks like a full git SHA or docker digest.
func isSHA(s string) bool {
	return shaRegex.MatchString(s) || strings.HasPrefix(s, "sha256:")
}

// syncCache is a thread-safe string→string cache.
type syncCache struct {
	mutex sync.Mutex
	items map[string]string
}

func newSyncCache() syncCache {
	return syncCache{items: make(map[string]string)}
}

func newSyncCachePtr() *syncCache {
	c := newSyncCache()
	return &c
}

func (cache *syncCache) getOrSet(key string, fetch func() (string, error)) (string, error) {
	cache.mutex.Lock()
	cached, ok := cache.items[key]
	cache.mutex.Unlock()
	if ok {
		return cached, nil
	}
	value, err := fetch()
	if err != nil {
		return "", err
	}
	cache.mutex.Lock()
	// Check again under lock in case another goroutine raced us.
	if existing, ok := cache.items[key]; ok {
		cache.mutex.Unlock()
		return existing, nil
	}
	cache.items[key] = value
	cache.mutex.Unlock()
	return value, nil
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

// unstableBranchExact are well-known branch names matched exactly.
var unstableBranchExact = map[string]bool{
	"main": true, "master": true, "develop": true, "development": true,
}

// unstableBranchPrefixes are branch prefixes matched with a slash separator,
// e.g. "feat/my-feature", "fix/issue-123", "hotfix/urgent".
var unstableBranchPrefixes = []string{"feat/", "fix/", "bug/", "hotfix/", "feature/", "bugfix/", "release/"}

// isUnstableBranch returns true if ref looks like a mutable branch name.
func isUnstableBranch(ref string) bool {
	lower := strings.ToLower(ref)
	if unstableBranchExact[lower] {
		return true
	}
	for _, prefix := range unstableBranchPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// warnBranchRef prints a red warning when a ref resolves to a known branch name.
func warnBranchRef(provider, action, ref string) {
	fmt.Fprintf(os.Stderr, "%s%sWARNING: %s@%s is a branch ref — the pinned SHA will become stale. Use a tag instead.%s\n",
		Ansi(AnsiBold), Ansi(AnsiRed), action, ref, Ansi(AnsiReset))
}

// warnDrift appends a drift warning to warns.
func warnDrift(kind, ref, tag, pinnedSHA, currentSHA string, warns *[]string) {
	*warns = append(*warns, fmt.Sprintf("%s%sWARNING: %s@%s has drifted — %s was mutated!%s\n  pinned:  %s\n  current: %s\n  → update this ref manually",
		Ansi(AnsiBold), Ansi(AnsiYellow), ref, tag, kind, Ansi(AnsiReset), pinnedSHA, currentSHA))
}

// actionPinner pins `uses: owner/repo@tag` refs to their SHAs using a shared cache.
// It is used by both the GitHub and Forgejo resolvers which share the same regex and
// output format, differing only in their fetch function and error prefix.
type actionPinner struct {
	name    string // used in error messages, e.g. "GitHub", "Forgejo"
	cache   *syncCache
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
		if isUnstableBranch(ref) {
			warnBranchRef(ap.name, action, ref)
		}
		repoPath := actionRepoPath(action)
		sha, err := ap.cache.getOrSet(repoPath+"@"+ref, func() (string, error) {
			return ap.resolve(repoPath, ref)
		})
		if err != nil {
			if isUnstableBranch(ref) {
				// Non-fatal: branch may not exist on this repo, already warned above.
				return "", false
			}
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
func (d *driftChecker) checkAll(content string, warns *[]string) {
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
			warnDrift(d.kind, ref, tag, pinnedSHA, currentSHA, warns)
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

// matchesAny returns true if s equals any element in the list.
func matchesAny(s string, list []string) bool {
	for _, v := range list {
		if s == v {
			return true
		}
	}
	return false
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

const maxRetryDelay = 10 * time.Minute

// retryDelay returns how long to wait before the next attempt.
// It reads Retry-After (seconds or HTTP-date) and X-RateLimit-Reset (unix timestamp).
func retryDelay(resp *http.Response) time.Duration {
	const fallback = 60 * time.Second
	if d, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
		return d
	}
	if d, ok := parseRateLimitReset(resp.Header.Get("X-RateLimit-Reset")); ok {
		return d
	}
	return fallback
}

func parseRetryAfter(h string) (time.Duration, bool) {
	if h == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(h); err == nil {
		d := time.Duration(secs) * time.Second
		if d > maxRetryDelay {
			d = maxRetryDelay
		}
		return d, true
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
	}
	return 0, false
}

func parseRateLimitReset(h string) (time.Duration, bool) {
	if h == "" {
		return 0, false
	}
	if unix, err := strconv.ParseInt(h, 10, 64); err == nil {
		if d := time.Until(time.Unix(unix, 0)); d > 0 {
			return d, true
		}
	}
	return 0, false
}
