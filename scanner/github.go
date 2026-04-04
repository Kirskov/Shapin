package scanner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

const githubJSONAccept = "application/vnd.github+json"

// githubActionRegex matches `uses: owner/repo@ref` or `uses: owner/repo/subdir@ref`
// and captures: full match, owner/repo[/subdir], ref
var githubActionRegex = regexp.MustCompile(`(uses:\s+)([a-zA-Z0-9_.-]+/[a-zA-Z0-9_./%-]+)@([^\s#]+)`)

// githubPinnedRegex matches already-pinned refs: `uses: action@sha # tag`
var githubPinnedRegex = regexp.MustCompile(`uses:\s+([a-zA-Z0-9_.-]+/[a-zA-Z0-9_./%-]+)@([0-9a-f]{40})\s+#\s+(\S+)`)

type githubResolver struct {
	token  string
	client *http.Client
	mu     sync.Mutex
	cache  map[string]string
	docker *dockerResolver
}

func newGitHubResolver(token string) *githubResolver {
	return newGitHubResolverWithClient(token, &http.Client{})
}

func newGitHubResolverWithClient(token string, client *http.Client) *githubResolver {
	return &githubResolver{
		token:  token,
		client: client,
		cache:  make(map[string]string),
		docker: newDockerResolver(""),
	}
}

func (r *githubResolver) Name() string { return "GitHub Actions" }

func (r *githubResolver) IsMatch(relPath string) bool {
	dir := slashDir(relPath)
	return (dir == ".github/workflows" || strings.HasPrefix(dir, ".github/workflows/")) &&
		isYAML(slashBase(relPath))
}

// Resolve replaces `uses: action@tag` and/or `image: name:tag` with pinned SHAs.
func (r *githubResolver) Resolve(content string, pinActions, pinImages bool) (string, error) {
	if pinImages {
		content = r.docker.resolveImages(content)
	}
	if pinActions {
		r.warnIfDrifted(content)
	}
	if !pinActions {
		return content, nil
	}
	return r.pinActions(content)
}

// warnIfDrifted scans for already-pinned refs and warns if the SHA no longer
// matches the tag. The file is never modified — the user must fix it manually.
func (r *githubResolver) warnIfDrifted(content string) {
	for _, parts := range githubPinnedRegex.FindAllStringSubmatch(content, -1) {
		action, pinnedSHA, tag := parts[1], parts[2], parts[3]
		repoPath := strings.Join(strings.SplitN(action, "/", 3)[:2], "/")
		currentSHA, err := r.fetchSHA(repoPath, tag)
		if err != nil {
			continue // best-effort, skip on error
		}
		if currentSHA != pinnedSHA {
			fmt.Printf("%s%sWARNING: %s@%s has drifted — tag was mutated!%s\n  pinned: %s\n  current: %s\n  → update this ref manually\n",
				colorBold, colorYellow, action, tag, colorReset, pinnedSHA, currentSHA)
		}
	}
}

// pinActions pins floating `uses: action@tag` refs to their SHAs.
func (r *githubResolver) pinActions(content string) (string, error) {
	var resolveErr error
	result := githubActionRegex.ReplaceAllStringFunc(content, func(match string) string {
		if resolveErr != nil {
			return match
		}
		parts := githubActionRegex.FindStringSubmatch(match)
		if len(parts) < 4 {
			return match
		}
		prefix, action, ref := parts[1], parts[2], parts[3]
		if isSHA(ref) {
			return match
		}
		repoPath := strings.Join(strings.SplitN(action, "/", 3)[:2], "/")
		sha, err := r.cachedSHA(repoPath, ref)
		if err != nil {
			resolveErr = fmt.Errorf("GitHub: %s@%s: %w", repoPath, ref, err)
			return match
		}
		return fmt.Sprintf("%s%s@%s # %s", prefix, action, sha, ref)
	})
	return result, resolveErr
}

// cachedSHA returns the SHA for repo@ref, fetching and caching it if needed.
func (r *githubResolver) cachedSHA(repoPath, ref string) (string, error) {
	cacheKey := repoPath + "@" + ref
	r.mu.Lock()
	sha, ok := r.cache[cacheKey]
	r.mu.Unlock()
	if ok {
		return sha, nil
	}
	sha, err := r.fetchSHA(repoPath, ref)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	r.cache[cacheKey] = sha
	r.mu.Unlock()
	return sha, nil
}

func (r *githubResolver) fetchSHA(repo, ref string) (string, error) {
	// Try as a tag first (most common case in CI), then as a branch/commit.
	sha, err := r.fetchTagSHA(repo, ref)
	if err == nil {
		return sha, nil
	}

	// Fall back to branch or commit SHA
	url := fmt.Sprintf("https://api.github.com/repos/%s/commits/%s", repo, ref)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	// application/vnd.github+json returns a full commit object with a "sha" field
	req.Header.Set("Accept", githubJSONAccept)
	if r.token != "" {
		req.Header.Set("Authorization", bearerPrefix+r.token)
	}

	resp, err := doWithRetry(r.client, req, 3)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d for ref %s", resp.StatusCode, ref)
	}

	var result struct {
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.SHA == "" {
		return "", fmt.Errorf("empty SHA returned")
	}
	return result.SHA, nil
}

func (r *githubResolver) fetchTagSHA(repo, tag string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/git/refs/tags/%s", repo, tag)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", githubJSONAccept)
	if r.token != "" {
		req.Header.Set("Authorization", bearerPrefix+r.token)
	}

	resp, err := doWithRetry(r.client, req, 3)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d for tag %s", resp.StatusCode, tag)
	}

	var result struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"object"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	// Annotated tags point to a tag object, not the commit — dereference it
	if result.Object.Type == "tag" {
		return r.fetchTagObjectSHA(result.Object.URL, r.token)
	}

	return result.Object.SHA, nil
}

func (r *githubResolver) fetchTagObjectSHA(url, token string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", githubJSONAccept)
	if token != "" {
		req.Header.Set("Authorization", bearerPrefix+token)
	}

	resp, err := doWithRetry(r.client, req, 3)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.Object.SHA, nil
}
