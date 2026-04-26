package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	DefaultForgejoHost  = "https://codeberg.org"
	forgejoActionsHost  = "https://code.forgejo.org"
	forgejoWorkflowsDir = ".forgejo/workflows"
)

type forgejoResolver struct {
	host   string
	token  string
	client *http.Client
	cache  *syncCache
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
		cache:  newSyncCachePtr(),
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
func (r *forgejoResolver) Resolve(content string, pinActions, pinImages bool) (string, []string, error) {
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
func (r *forgejoResolver) fixAndWarnDrifted(content string, warns *[]string) string {
	dc := &driftChecker{
		pinnedRegex: githubPinnedRegex,
		kind:        "tag",
		resolve:     r.fetchSHA,
		repoPath:    actionRepoPath,
	}
	dc.checkAll(content, warns)
	return dc.fixDrift(content)
}

func (r *forgejoResolver) pinActions(content string) (string, error) {
	return (&actionPinner{name: "Forgejo", cache: r.cache, resolve: r.fetchSHA}).pin(content)
}

// fetchSHA tries the configured host first, then falls back to data.forgejo.org
// (the default Forgejo actions registry) for short refs like actions/checkout.
func (r *forgejoResolver) fetchSHA(repo, ref string) (string, error) {
	sha, err := r.fetchTagSHA(r.host, repo, ref)
	if err == nil {
		return sha, nil
	}
	// Fall back to the Forgejo actions registry for community actions.
	if r.host != forgejoActionsHost {
		if sha, err2 := r.fetchTagSHA(forgejoActionsHost, repo, ref); err2 == nil {
			return sha, nil
		}
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

func (r *forgejoResolver) fetchTagSHA(host, repo, tag string) (string, error) {
	url := fmt.Sprintf("%s/api/v1/repos/%s/git/refs/tags/%s", host, repo, tag)
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
