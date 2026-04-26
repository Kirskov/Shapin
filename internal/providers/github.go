package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	githubAPIBase      = "https://api.github.com"
	githubJSONAccept   = "application/vnd.github+json"
	githubWorkflowsDir = ".github/workflows"
)

// githubActionRegex matches `uses: owner/repo@ref` or `uses: owner/repo/subdir@ref`
// and captures: full match, owner/repo[/subdir], ref
var githubActionRegex = mustCompile(patternGHAction)

// githubPinnedRegex matches already-pinned refs: `uses: action@sha # tag`
var githubPinnedRegex = mustCompile(patternGHPinned)

type githubResolver struct {
	token  string
	client *http.Client
	cache  *syncCache
	docker *dockerResolver
}

func NewGitHubResolver(token string) *githubResolver {
	return NewGitHubResolverWithClient(token, newHTTPClient())
}

func NewGitHubResolverWithClient(token string, client *http.Client) *githubResolver {
	return &githubResolver{
		token:  token,
		client: client,
		cache:  newSyncCachePtr(),
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
func (r *githubResolver) Resolve(content string, pinActions, pinImages bool) (string, []string, error) {
	var warns []string
	if pinImages {
		content = r.docker.resolveImages(content, &warns)
	}
	if !pinActions {
		return content, warns, nil
	}
	content = r.fixAndWarnDrifted(content, &warns)
	result, err := r.pinActions(content)
	return result, warns, err
}

// fixAndWarnDrifted updates drifted pinned refs to their current SHA and appends
// warnings for each one found.
func (r *githubResolver) fixAndWarnDrifted(content string, warns *[]string) string {
	dc := &driftChecker{
		pinnedRegex: githubPinnedRegex,
		kind:        "tag",
		resolve:     r.fetchSHA,
		repoPath:    actionRepoPath,
	}
	dc.checkAll(content, warns)
	return dc.fixDrift(content)
}

// pinActions pins floating `uses: action@tag` refs to their SHAs.
func (r *githubResolver) pinActions(content string) (string, error) {
	return (&actionPinner{name: "GitHub", cache: r.cache, resolve: r.fetchSHA}).pin(content)
}

func (r *githubResolver) fetchSHA(repo, ref string) (string, error) {
	// Try as a tag first (most common case in CI), then as a branch/commit.
	sha, err := r.fetchTagSHA(repo, ref)
	if err == nil {
		return sha, nil
	}

	// Fall back to branch or commit SHA
	url := fmt.Sprintf("%s/repos/%s/commits/%s", githubAPIBase, repo, ref)
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
	url := fmt.Sprintf("%s/repos/%s/git/refs/tags/%s", githubAPIBase, repo, tag)
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
		if !strings.HasPrefix(result.Object.URL, githubAPIBase) {
			return "", fmt.Errorf("unexpected tag object URL: %s", result.Object.URL)
		}
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
