package scanner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	githubJSONAccept    = "application/vnd.github+json"
	githubWorkflowsDir  = ".github/workflows"
)

// githubActionRegex matches `uses: owner/repo@ref` or `uses: owner/repo/subdir@ref`
// and captures: full match, owner/repo[/subdir], ref
var githubActionRegex = mustCompile(patternGHAction)

// githubPinnedRegex matches already-pinned refs: `uses: action@sha # tag`
var githubPinnedRegex = mustCompile(patternGHPinned)

type githubResolver struct {
	token  string
	client *http.Client
	cache  syncCache
	docker *dockerResolver
}

func newGitHubResolver(token string) *githubResolver {
	return newGitHubResolverWithClient(token, newHTTPClient())
}

func newGitHubResolverWithClient(token string, client *http.Client) *githubResolver {
	return &githubResolver{
		token:  token,
		client: client,
		cache:  newSyncCache(),
		docker: newDockerResolver(""),
	}
}

func (r *githubResolver) Name() string { return "GitHub Actions" }

func (r *githubResolver) IsMatch(relPath string) bool {
	dir := slashDir(relPath)
	return (dir == githubWorkflowsDir || strings.HasPrefix(dir, githubWorkflowsDir+"/")) &&
		isYAML(slashBase(relPath))
}

// Resolve replaces `uses: action@tag` and/or `image: name:tag` with pinned SHAs.
func (r *githubResolver) Resolve(content string, pinActions, pinImages bool) (string, error) {
	if pinImages {
		content = r.docker.resolveImages(content)
	}
	if !pinActions {
		return content, nil
	}
	r.warnIfDrifted(content)
	return r.pinActions(content)
}

// warnIfDrifted scans for already-pinned refs and warns if the SHA no longer
// matches the tag. The file is never modified — the user must fix it manually.
func (r *githubResolver) warnIfDrifted(content string) {
	(&driftChecker{
		pinnedRegex: githubPinnedRegex,
		kind:        "tag",
		resolve:     r.fetchSHA,
		repoPath:    actionRepoPath,
	}).checkAll(content)
}

// pinActions pins floating `uses: action@tag` refs to their SHAs.
func (r *githubResolver) pinActions(content string) (string, error) {
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
		sha, err := r.cache.getOrSet(repoPath+"@"+ref, func() (string, error) { return r.fetchSHA(repoPath, ref) })
		if err != nil {
			resolveErr = fmt.Errorf("GitHub: %s@%s: %w", repoPath, ref, err)
			return "", false
		}
		return fmt.Sprintf("%s%s@%s # %s", prefix, action, sha, ref), true
	})
	return result, resolveErr
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

	resp, err := doWithRetry(r.client, req)
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

	resp, err := doWithRetry(r.client, req)
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
		return r.fetchTagObjectSHA(result.Object.URL)
	}

	return result.Object.SHA, nil
}

func (r *githubResolver) fetchTagObjectSHA(url string) (string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", githubJSONAccept)
	if r.token != "" {
		req.Header.Set("Authorization", bearerPrefix+r.token)
	}

	resp, err := doWithRetry(r.client, req)
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
	if result.Object.SHA == "" {
		return "", fmt.Errorf("empty SHA returned for tag object")
	}
	return result.Object.SHA, nil
}
