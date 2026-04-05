package scanner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"gopkg.in/yaml.v3"
)

const (
	gitlabCIRootPrefix = ".gitlab-ci"
	gitlabDir          = ".gitlab"
)

// gitlabComponentRegex matches GitLab CI components:
// `- component: gitlab.com/group/project/component@tag`
var gitlabComponentRegex = mustCompile(patternGLComponent)

// gitlabInputTagRegex matches an input key containing TAG with an image:tag value.
// e.g. `      TRIVY_TAG: "aquasec/trivy:0.69.3"`
var gitlabInputTagRegex = mustCompile(patternGLInputTag)

// gitlabPinnedRegex matches already-pinned component refs: `component: path@sha # tag`
var gitlabPinnedRegex = mustCompile(patternGLPinned)

type gitlabResolver struct {
	host   string
	token  string
	client *http.Client
	cache  syncCache
	docker *dockerResolver
}

func newGitLabResolver(host, token string) *gitlabResolver {
	return &gitlabResolver{
		host:   host,
		token:  token,
		client: newHTTPClient(),
		cache:  newSyncCache(),
		docker: newDockerResolver(token),
	}
}

func (r *gitlabResolver) Name() string { return "GitLab CI" }

func (r *gitlabResolver) IsMatch(relPath string) bool {
	dir := slashDir(relPath)
	name := slashBase(relPath)

	if dir == "." && (name == gitlabCIRootPrefix+".yml" || name == gitlabCIRootPrefix+".yaml" ||
		strings.HasPrefix(name, gitlabCIRootPrefix+"-") && isYAML(name)) {
		return true
	}

	return dir == gitlabDir || strings.HasPrefix(dir, gitlabDir+"/") && isYAML(name)
}

// resolveComponentInputs pins image:tag values in inputs: blocks whose key
// contains "TAG" (e.g. TRIVY_TAG, IMAGE_TAG). Uses yaml.v3 to identify which
// keys are TAG inputs, then regex-replaces to preserve file formatting.
func (r *gitlabResolver) resolveComponentInputs(content string) string {
	tagKeys := collectTagInputKeys(content)
	if len(tagKeys) == 0 {
		return content
	}
	return gitlabInputTagRegex.ReplaceAllStringFunc(content, r.pinInputTagMatch(tagKeys))
}

// collectTagInputKeys parses the YAML and returns the set of input key names
// that contain "TAG" and hold an unpinned image:tag value.
// It walks the entire document recursively so it catches variables: and inputs:
// at any nesting level (top-level, inside jobs, inside include blocks, etc.).
func collectTagInputKeys(content string) map[string]bool {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil || root.Kind == 0 {
		return nil
	}
	keys := make(map[string]bool)
	walkTagMaps(&root, keys)
	return keys
}

// walkTagMaps recursively walks a yaml.Node tree and collects keys that
// contain "TAG" from any "variables" or "inputs" mapping node.
func walkTagMaps(node *yaml.Node, keys map[string]bool) {
	if node == nil {
		return
	}
	if node.Kind == yaml.DocumentNode {
		for _, child := range node.Content {
			walkTagMaps(child, keys)
		}
		return
	}
	if node.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(node.Content); i += 2 {
			keyNode := node.Content[i]
			valNode := node.Content[i+1]
			if keyNode.Kind == yaml.ScalarNode &&
				(keyNode.Value == "variables" || keyNode.Value == "inputs") &&
				valNode.Kind == yaml.MappingNode {
				collectTagKeysFromMap(valNode, keys)
			}
			// Recurse into all values regardless
			walkTagMaps(valNode, keys)
		}
		return
	}
	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			walkTagMaps(child, keys)
		}
	}
}

// collectTagKeysFromMap adds keys containing "TAG" with an image:tag value
// from a mapping node into keys.
func collectTagKeysFromMap(node *yaml.Node, keys map[string]bool) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		k := node.Content[i].Value
		v := node.Content[i+1].Value
		if strings.Contains(strings.ToUpper(k), "TAG") && !isSHA(v) && strings.Contains(v, ":") {
			keys[k] = true
		}
	}
}

// pinInputTagMatch returns a replacement function for gitlabInputTagRegex that
// pins values whose key is in tagKeys.
func (r *gitlabResolver) pinInputTagMatch(tagKeys map[string]bool) func(string) string {
	return func(match string) string {
		parts := gitlabInputTagRegex.FindStringSubmatch(match)
		if len(parts) < 5 {
			return match
		}
		prefix, image, tag := parts[1], parts[2], parts[3]
		key := strings.TrimSpace(strings.SplitN(prefix, ":", 2)[0])

		if !tagKeys[key] || isSHA(tag) || tag == "latest" {
			return match
		}

		digest, err := r.docker.fetchDigest(image, tag)
		if err != nil {
			fmt.Printf("  warn: GitLab input %s (%s:%s): %v\n", key, image, tag, err)
			return match
		}

		indent := prefix[:len(prefix)-len(strings.TrimLeft(prefix, " \t"))]
		return fmt.Sprintf("%s%s: %s@%s # %s", indent, key, image, digest, tag)
	}
}

// Resolve replaces image tags and/or component refs with their SHAs.
func (r *gitlabResolver) Resolve(content string, pinActions, pinImages bool) (string, error) {
	var resolveErr error

	result := content
	if pinImages {
		result = r.docker.resolveImages(result)
		result = r.resolveComponentInputs(result)
	}

	if !pinActions {
		return result, nil
	}

	r.warnIfDrifted(content)

	// Replace component: host/group/project/comp@tag → @sha
	result = replaceMatches(gitlabComponentRegex, result, func(parts []string) (string, bool) {
		if resolveErr != nil {
			return "", false
		}
		prefix, component, ref := parts[1], parts[2], parts[3]
		if isSHA(ref) {
			return "", false
		}
		sha, err := r.cache.getOrSet("component:"+component+"@"+ref, func() (string, error) {
			return r.fetchComponentSHA(component, ref)
		})
		if err != nil {
			fmt.Printf("  warn: GitLab component %s@%s: %v\n", component, ref, err)
			return "", false
		}
		return fmt.Sprintf("%s%s@%s # %s", prefix, component, sha, ref), true
	})

	return result, resolveErr
}

// warnIfDrifted checks already-pinned component refs and warns if the SHA has
// changed. The file is never modified — the user must fix it manually.
func (r *gitlabResolver) warnIfDrifted(content string) {
	(&driftChecker{
		pinnedRegex: gitlabPinnedRegex,
		kind:        "ref",
		resolve:     r.fetchComponentSHA,
	}).checkAll(content)
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

	resp, err := doWithRetry(r.client, req)
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
