package providers

import (
	"encoding/json"
	"fmt"
	"maps"
	"gopkg.in/yaml.v3"
	"net/http"
	"os"
	"strings"
)

const (
	gitlabCIRootPrefix = ".gitlab-ci"
	gitlabDir          = ".gitlab"
)

var gitlabCIRootFiles = []string{
	gitlabCIRootPrefix + ".yml",
	gitlabCIRootPrefix + ".yaml",
}

// gitlabInputTagRegex matches an input key containing TAG with an image:tag value.
// e.g. `      TRIVY_TAG: "aquasec/trivy:0.69.3"`
var gitlabInputTagRegex = mustCompile(patternGLInputTag)

// gitlabMappedVersionRegex matches a bare version input whose key has a known stem.
// e.g. `  TF_VERSION: "1.14.8"` or `  TF_VERSION: 1.14.8`
var gitlabMappedVersionRegex = mustCompile(patternGLMappedVersion)

// gitlabComponentRegex matches GitLab CI component refs:
// `component: gitlab.com/group/project/name@ref`
var gitlabComponentRegex = mustCompile(patternGLComponent)

// gitlabPinnedComponentRegex matches already-pinned component refs.
var gitlabPinnedComponentRegex = mustCompile(patternGLPinned)

const defaultGitLabHost = "https://gitlab.com"

type gitlabResolver struct {
	host        string
	token       string
	client      *http.Client
	cache       *syncCache
	docker      *dockerResolver
	tagMappings map[string]string // stem → image, merges builtins + user overrides
}

func NewGitLabResolver(host, token string, userMappings map[string]string) *gitlabResolver {
	if host == "" {
		host = defaultGitLabHost
	}
	merged := make(map[string]string, len(builtinStemMappings)+len(userMappings))
	maps.Copy(merged, builtinStemMappings)
	// User-supplied mappings override builtins.
	for k, v := range userMappings {
		merged[strings.ToUpper(k)] = v
	}
	return &gitlabResolver{
		host:        host,
		token:       token,
		client:      newHTTPClient(),
		cache:       newSyncCachePtr(),
		docker:      newDockerResolver(token),
		tagMappings: merged,
	}
}

func (r *gitlabResolver) Name() string { return "GitLab CI" }

func (r *gitlabResolver) IsMatch(relPath string) bool {
	dir := slashDir(relPath)
	name := slashBase(relPath)

	// Match .gitlab-ci.yml / .gitlab-ci.yaml / .gitlab-ci-*.yml at any directory
	// level, not just the scan root — supports monorepos where each subdirectory
	// is its own project.
	if matchesAny(name, gitlabCIRootFiles) ||
		strings.HasPrefix(name, gitlabCIRootPrefix+"-") && isYAML(name) {
		return true
	}

	// Match any .yml/.yaml inside a .gitlab/ directory at any depth.
	return (dir == gitlabDir || strings.HasPrefix(dir, gitlabDir+"/") ||
		strings.Contains(dir, "/"+gitlabDir+"/") ||
		strings.HasSuffix(dir, "/"+gitlabDir)) && isYAML(name)
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
			fmt.Fprintf(os.Stderr, "  warn: GitLab input %s (%s:%s): %v\n", key, image, tag, err)
			return match
		}

		indent := prefix[:len(prefix)-len(strings.TrimLeft(prefix, " \t"))]
		return fmt.Sprintf("%s%s: %s@%s # %s:%s", indent, key, image, digest, image, tag)
	}
}

// collectMappedVersionKeys parses the YAML and returns keys from variables:/inputs:
// blocks that have a stem in tagMappings and a bare, unpinned version value.
func (r *gitlabResolver) collectMappedVersionKeys(content string) map[string]bool {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil || root.Kind == 0 {
		return nil
	}
	keys := make(map[string]bool)
	r.walkMappedVersionMaps(&root, keys)
	return keys
}

func (r *gitlabResolver) walkMappedVersionMaps(node *yaml.Node, keys map[string]bool) {
	if node == nil {
		return
	}
	if node.Kind == yaml.DocumentNode {
		for _, child := range node.Content {
			r.walkMappedVersionMaps(child, keys)
		}
		return
	}
	if node.Kind == yaml.MappingNode {
		// Collect eligible keys from every mapping node — mapped-version inputs
		// can appear at any depth, not only inside variables:/inputs: blocks.
		r.collectMappedVersionKeysFromMap(node, keys)
		for i := 0; i+1 < len(node.Content); i += 2 {
			r.walkMappedVersionMaps(node.Content[i+1], keys)
		}
		return
	}
	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			r.walkMappedVersionMaps(child, keys)
		}
	}
}

func (r *gitlabResolver) collectMappedVersionKeysFromMap(node *yaml.Node, keys map[string]bool) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		k := node.Content[i].Value
		v := node.Content[i+1].Value
		stem := extractStem(k)
		if stem == "" {
			continue
		}
		if r.lookupStem(stem) == "" {
			continue
		}
		// Skip: CI variable interpolation, already a digest, image:tag format, empty
		if v == "" || strings.HasPrefix(v, "$") || isSHA(v) || strings.Contains(v, ":") {
			continue
		}
		keys[k] = true
	}
}

// lookupStem finds the image for a stem by trying progressively shorter
// prefixes. e.g. NODE_IMAGE → NODE_IMAGE, then NODE.
// Returns the image name, or "" if no mapping is found.
func (r *gitlabResolver) lookupStem(stem string) string {
	s := stem
	for {
		if image, ok := r.tagMappings[s]; ok {
			return image
		}
		idx := strings.LastIndex(s, "_")
		if idx < 0 {
			return ""
		}
		s = s[:idx]
	}
}

// resolveMappedVersionInputs pins bare version values (e.g. TF_VERSION: "1.14.8")
// by looking up the key's stem in tagMappings to find the image, then fetching
// the digest for that version. Uses YAML parsing to identify eligible keys, then
// regex replacement to preserve file formatting.
func (r *gitlabResolver) resolveMappedVersionInputs(content string) string {
	mappedKeys := r.collectMappedVersionKeys(content)
	if len(mappedKeys) == 0 {
		return content
	}
	return gitlabMappedVersionRegex.ReplaceAllStringFunc(content, func(match string) string {
		parts := gitlabMappedVersionRegex.FindStringSubmatch(match)
		if len(parts) < 6 {
			return match
		}
		indent, key, quoteOpen, version, quoteClose := parts[1], parts[2], parts[3], parts[4], parts[5]
		if !mappedKeys[key] {
			return match
		}
		stem := extractStem(key)
		image := r.lookupStem(stem)
		digest, err := r.docker.fetchDigest(image, version)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  warn: GitLab input %s (%s:%s): %v\n", key, image, version, err)
			return match
		}
		digestKey := toDigestKey(key)
		return fmt.Sprintf("%s%s: %s%s%s # %s:%s", indent, digestKey, quoteOpen, digest, quoteClose, image, version)
	})
}

// gitlabHostVars are predefined GitLab CI variables that expand to the instance hostname.
// They are replaced with the configured host so component paths can be resolved.
var gitlabHostVars = []string{"$CI_SERVER_FQDN", "$CI_SERVER_HOST"}

// resolveHostVar replaces a known GitLab host variable at the start of a
// component path with the bare hostname of r.host (e.g. "gitlab.com").
func (r *gitlabResolver) resolveHostVar(component string) string {
	host := strings.TrimPrefix(r.host, "https://")
	host = strings.TrimPrefix(host, "http://")
	for _, v := range gitlabHostVars {
		if strings.HasPrefix(component, v+"/") {
			return host + component[len(v):]
		}
	}
	return component
}

// pinComponents pins `component: path@ref` refs to their commit SHAs.
func (r *gitlabResolver) pinComponents(content string) (string, error) {
	var resolveErr error
	result := replaceMatches(gitlabComponentRegex, content, func(parts []string) (string, bool) {
		if resolveErr != nil {
			return "", false
		}
		prefix, component, ref := parts[1], parts[2], parts[3]
		if isSHA(ref) {
			return "", false
		}
		// Resolve known GitLab host variables to the configured host.
		resolved := r.resolveHostVar(component)
		// Skip if path still contains a variable we can't resolve.
		if strings.HasPrefix(resolved, "$") {
			return "", false
		}
		sha, err := r.cache.getOrSet("component:"+resolved+"@"+ref, func() (string, error) {
			return r.fetchComponentSHA(resolved, ref)
		})
		if err != nil {
			msg := fmt.Sprintf("  warn: GitLab component %s@%s: %v", component, ref, err)
			if strings.Contains(err.Error(), "HTTP 404") {
				msg += " — try --gitlab-token if this is a private component"
			}
			fmt.Fprintln(os.Stderr, msg)
			return "", false
		}
		if isUnstableBranch(ref) {
			warnBranchRef("GitLab", component, ref)
		}
		return fmt.Sprintf("%s%s@%s # %s", prefix, component, sha, ref), true
	})
	return result, resolveErr
}

// warnIfDrifted checks already-pinned component refs and warns if the SHA has changed.
func (r *gitlabResolver) warnIfDrifted(content string) {
	(&driftChecker{
		pinnedRegex: gitlabPinnedComponentRegex,
		kind:        "ref",
		resolve:     r.fetchComponentSHA,
	}).checkAll(content)
}

// fetchComponentSHA resolves a component ref to a commit SHA.
// It tries the tags API first (no token needed for public projects),
// then falls back to the commits API.
func (r *gitlabResolver) fetchComponentSHA(component, ref string) (string, error) {
	projectPath, err := extractProjectPath(component)
	if err != nil {
		return "", err
	}
	encoded := strings.ReplaceAll(projectPath, "/", "%2F")

	// Try tags API first — works without a token for public projects.
	if sha, err := r.fetchTagSHA(encoded, ref); err == nil {
		return sha, nil
	}

	// Fall back to commits API (covers branches and plain SHAs).
	return r.fetchCommitSHA(encoded, ref)
}

// gitlabGet performs an authenticated GET to the GitLab API and decodes the
// JSON response body into out. Returns an error if the status is not 200.
func (r *gitlabResolver) gitlabGet(url string, out any) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	if r.token != "" {
		req.Header.Set("PRIVATE-TOKEN", r.token)
	}
	resp, err := doWithRetry(r.client, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (r *gitlabResolver) fetchTagSHA(encodedProject, tag string) (string, error) {
	url := fmt.Sprintf("%s/api/v4/projects/%s/repository/tags/%s", r.host, encodedProject, tag)
	var result struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := r.gitlabGet(url, &result); err != nil {
		return "", fmt.Errorf("tag %s: %w", tag, err)
	}
	if result.Commit.ID == "" {
		return "", fmt.Errorf("empty SHA for tag %s", tag)
	}
	return result.Commit.ID, nil
}

func (r *gitlabResolver) fetchCommitSHA(encodedProject, ref string) (string, error) {
	url := fmt.Sprintf("%s/api/v4/projects/%s/repository/commits/%s", r.host, encodedProject, ref)
	var result struct {
		ID string `json:"id"`
	}
	if err := r.gitlabGet(url, &result); err != nil {
		return "", fmt.Errorf("ref %s: %w", ref, err)
	}
	if result.ID == "" {
		return "", fmt.Errorf("empty SHA for ref %s", ref)
	}
	return result.ID, nil
}

// extractProjectPath extracts "group/project" from a component path like
// "gitlab.com/group/project/component". The first segment must contain a dot
// (i.e. be a hostname); paths without a hostname are invalid per the GitLab CI spec.
func extractProjectPath(component string) (string, error) {
	parts := strings.Split(component, "/")
	if len(parts) < 1 || !strings.Contains(parts[0], ".") {
		return "", fmt.Errorf("component path must start with a hostname (e.g. gitlab.com/...): %s", component)
	}
	if len(parts) < 3 {
		return "", fmt.Errorf("component path too short, expected hostname/group/project/name: %s", component)
	}
	return parts[1] + "/" + parts[2], nil
}

// Resolve replaces image tags, bare version inputs, and component refs to digests/SHAs.
func (r *gitlabResolver) Resolve(content string, pinActions, pinImages bool) (string, error) {
	result := content
	if pinImages {
		result = r.docker.resolveImages(result)
		result = r.docker.resolveImageNames(result)
		result = r.docker.resolveServices(result)
		result = r.resolveComponentInputs(result)
		result = r.resolveMappedVersionInputs(result)
	}
	if pinActions {
		r.warnIfDrifted(result)
		var err error
		result, err = r.pinComponents(result)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}
