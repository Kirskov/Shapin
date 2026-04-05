package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	DefaultForgejoHost    = "https://codeberg.org"
	forgejoWorkflowsDir   = ".forgejo/workflows"
)

type forgejoResolver struct {
	host   string
	token  string
	client *http.Client
	cache  syncCache
	docker *dockerResolver
}

func NewForgejoResolver(host, token string) *forgejoResolver {
	if host == "" {
		host = DefaultForgejoHost
	}
	return &forgejoResolver{
		host:   host,
		token:  token,
		client: newHTTPClient(),
		cache:  newSyncCache(),
		docker: newDockerResolver(""),
	}
}

func (r *forgejoResolver) Name() string { return "Forgejo Actions" }

func (r *forgejoResolver) IsMatch(relPath string) bool {
	dir := slashDir(relPath)
	return (dir == forgejoWorkflowsDir || strings.HasPrefix(dir, forgejoWorkflowsDir+"/")) &&
		isYAML(slashBase(relPath))
}

// Resolve pins `uses: owner/repo@tag` refs and Docker image: tags.
func (r *forgejoResolver) Resolve(content string, pinActions, pinImages bool) (string, error) {
	if pinImages {
		content = r.docker.resolveImages(content)
	}
	if !pinActions {
		return content, nil
	}
	r.warnIfDrifted(content)
	return r.pinActions(content)
}

// warnIfDrifted checks already-pinned refs and warns if the SHA has changed.
// The file is never modified — the user must fix it manually.
func (r *forgejoResolver) warnIfDrifted(content string) {
	(&driftChecker{
		pinnedRegex: githubPinnedRegex,
		kind:        "tag",
		resolve:     r.fetchSHA,
		repoPath:    actionRepoPath,
	}).checkAll(content)
}

func (r *forgejoResolver) pinActions(content string) (string, error) {
	return (&actionPinner{name: "Forgejo", cache: r.cache, resolve: r.fetchSHA}).pin(content)
}

// fetchSHA tries tag first, then falls back to branch/commit.
func (r *forgejoResolver) fetchSHA(repo, ref string) (string, error) {
	sha, err := r.fetchTagSHA(repo, ref)
	if err == nil {
		return sha, nil
	}
	url := fmt.Sprintf("%s/api/v1/repos/%s/commits/%s", r.host, repo, ref)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
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

func (r *forgejoResolver) fetchTagSHA(repo, tag string) (string, error) {
	url := fmt.Sprintf("%s/api/v1/repos/%s/git/refs/tags/%s", r.host, repo, tag)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
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
	// Gitea/Forgejo returns an array of refs
	var refs []struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&refs); err != nil {
		return "", err
	}
	if len(refs) == 0 {
		return "", fmt.Errorf("no ref found for tag %s", tag)
	}
	return refs[0].Object.SHA, nil
}
