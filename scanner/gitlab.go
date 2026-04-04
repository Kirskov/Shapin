package scanner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

// gitlabComponentRegex matches GitLab CI components:
// `- component: gitlab.com/group/project/component@tag`
var gitlabComponentRegex = regexp.MustCompile(`(component:\s+)([a-zA-Z0-9_.\-/]+)@([^\s#]+)`)

// gitlabPinnedRegex matches already-pinned component refs: `component: path@sha # tag`
var gitlabPinnedRegex = regexp.MustCompile(`component:\s+([a-zA-Z0-9_.\-/]+)@([0-9a-f]{40})\s+#\s+(\S+)`)

type gitlabResolver struct {
	host   string
	token  string
	client *http.Client
	mu     sync.Mutex
	cache  map[string]string
	docker *dockerResolver
}

func newGitLabResolver(host, token string) *gitlabResolver {
	return &gitlabResolver{
		host:   host,
		token:  token,
		client: &http.Client{},
		cache:  make(map[string]string),
		docker: newDockerResolver(token),
	}
}

func (r *gitlabResolver) Name() string { return "GitLab CI" }

func (r *gitlabResolver) IsMatch(relPath string) bool {
	dir := slashDir(relPath)
	name := slashBase(relPath)

	if dir == "." && (name == ".gitlab-ci.yml" || name == ".gitlab-ci.yaml" ||
		strings.HasPrefix(name, ".gitlab-ci-") && isYAML(name)) {
		return true
	}

	return dir == ".gitlab" || strings.HasPrefix(dir, ".gitlab/") && isYAML(name)
}

// Resolve replaces image tags and/or component refs with their SHAs.
func (r *gitlabResolver) Resolve(content string, pinActions, pinImages bool) (string, error) {
	var resolveErr error

	result := content
	if pinImages {
		result = r.docker.resolveImages(content)
	}

	if pinActions {
		r.warnIfDrifted(content)
	}

	if !pinActions {
		return result, nil
	}

	// Replace component: host/group/project/comp@tag → @sha
	result = gitlabComponentRegex.ReplaceAllStringFunc(result, func(match string) string {
		if resolveErr != nil {
			return match
		}

		parts := gitlabComponentRegex.FindStringSubmatch(match)
		if len(parts) < 4 {
			return match
		}
		prefix := parts[1]    // "component: "
		component := parts[2] // "gitlab.com/group/project/comp"
		ref := parts[3]       // "v1.0"

		if isSHA(ref) {
			return match
		}

		cacheKey := "component:" + component + "@" + ref
		r.mu.Lock()
		sha, ok := r.cache[cacheKey]
		r.mu.Unlock()
		if !ok {
			var err error
			sha, err = r.fetchComponentSHA(component, ref)
			if err != nil {
				fmt.Printf("  warn: GitLab component %s@%s: %v\n", component, ref, err)
				return match
			}
			r.mu.Lock()
			r.cache[cacheKey] = sha
			r.mu.Unlock()
		}

		return fmt.Sprintf("%s%s@%s # %s", prefix, component, sha, ref)
	})

	return result, resolveErr
}

// warnIfDrifted checks already-pinned component refs and warns if the SHA has
// changed. The file is never modified — the user must fix it manually.
func (r *gitlabResolver) warnIfDrifted(content string) {
	for _, parts := range gitlabPinnedRegex.FindAllStringSubmatch(content, -1) {
		component, pinnedSHA, tag := parts[1], parts[2], parts[3]
		currentSHA, err := r.fetchComponentSHA(component, tag)
		if err != nil {
			continue
		}
		if currentSHA != pinnedSHA {
			fmt.Printf("%s%sWARNING: component %s@%s has drifted — ref was mutated!%s\n  pinned: %s\n  current: %s\n  → update this ref manually\n",
				colorBold, colorYellow, component, tag, colorReset, pinnedSHA, currentSHA)
		}
	}
}

// fetchComponentSHA resolves a GitLab CI component ref to a commit SHA.
func (r *gitlabResolver) fetchComponentSHA(component, ref string) (string, error) {
	// component format: gitlab.com/group/project/path@ref
	// We need: group/project as the project path, ref as the tag/branch
	projectPath := extractProjectPath(component)
	if projectPath == "" {
		return "", fmt.Errorf("cannot parse component path: %s", component)
	}

	// GitLab API expects group%2Fproject (slash encoded) as the project id
	encodedPath := strings.ReplaceAll(projectPath, "/", "%2F")
	encodedRef := strings.ReplaceAll(ref, "/", "%2F")
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/commits/%s", r.host, encodedPath, encodedRef)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return "", err
	}
	if r.token != "" {
		req.Header.Set("PRIVATE-TOKEN", r.token)
	}

	resp, err := doWithRetry(r.client, req, 3)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	return result.ID, nil
}

// splitRegistryAndRepo splits "registry.gitlab.com/group/image" into
// ("registry.gitlab.com", "group/image"). Defaults to Docker Hub.
func splitRegistryAndRepo(image string) (host, repo string) {
	parts := strings.SplitN(image, "/", 2)
	if len(parts) == 2 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")) {
		return parts[0], parts[1]
	}
	if !strings.Contains(image, "/") {
		return "registry-1.docker.io", "library/" + image
	}
	return "registry-1.docker.io", image
}

// extractProjectPath extracts "group/project" from a component path like
// "gitlab.com/group/project/component" or "group/project/component".
func extractProjectPath(component string) string {
	parts := strings.Split(component, "/")
	start := 0
	if strings.Contains(parts[0], ".") {
		start = 1
	}
	if len(parts) < start+2 {
		return ""
	}
	return parts[start] + "/" + parts[start+1]
}
