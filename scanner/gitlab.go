package scanner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// gitlabComponentRegex matches GitLab CI components:
// `- component: gitlab.com/group/project/component@tag`
var gitlabComponentRegex = regexp.MustCompile(`(component:\s+)([a-zA-Z0-9_.\-/]+)@([^\s#]+)`)

type gitlabResolver struct {
	host   string
	token  string
	client *http.Client
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

// resolve replaces image tags and/or component refs with their SHAs.
func (r *gitlabResolver) resolve(content string, pinActions, pinImages bool) (string, error) {
	var resolveErr error

	result := content
	if pinImages {
		result = r.docker.resolveImages(content)
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
		sha, ok := r.cache[cacheKey]
		if !ok {
			var err error
			sha, err = r.fetchComponentSHA(component, ref)
			if err != nil {
				fmt.Printf("  warn: GitLab component %s@%s: %v\n", component, ref, err)
				return match
			}
			r.cache[cacheKey] = sha
		}

		return fmt.Sprintf("%s%s@%s # %s", prefix, component, sha, ref)
	})

	return result, resolveErr
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

	resp, err := r.client.Do(req)
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
