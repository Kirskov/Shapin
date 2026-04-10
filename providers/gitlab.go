package providers

import (
	"fmt"
	"strings"
	"gopkg.in/yaml.v3"
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

type gitlabResolver struct {
	docker      *dockerResolver
	tagMappings map[string]string // stem → image, merges builtins + user overrides
}

func NewGitLabResolver(host, token string, userMappings map[string]string) *gitlabResolver {
	merged := make(map[string]string, len(builtinStemMappings)+len(userMappings))
	for k, v := range builtinStemMappings {
		merged[k] = v
	}
	// User-supplied mappings override builtins.
	for k, v := range userMappings {
		merged[strings.ToUpper(k)] = v
	}
	return &gitlabResolver{
		docker:      newDockerResolver(token),
		tagMappings: merged,
	}
}

func (r *gitlabResolver) Name() string { return "GitLab CI" }

func (r *gitlabResolver) IsMatch(relPath string) bool {
	dir := slashDir(relPath)
	name := slashBase(relPath)

	if dir == "." && (matchesAny(name, gitlabCIRootFiles) ||
		strings.HasPrefix(name, gitlabCIRootPrefix+"-") && isYAML(name)) {
		return true
	}

	return (dir == gitlabDir || strings.HasPrefix(dir, gitlabDir+"/")) && isYAML(name)
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

// resolveMappedVersionInputs pins bare version values (e.g. TF_VERSION: "1.14.8")
// by looking up the key's stem in tagMappings to find the image, then fetching
// the digest for that version. Values starting with "$" (CI variable interpolation)
// or already containing a digest are skipped.
func (r *gitlabResolver) resolveMappedVersionInputs(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		colonIdx := strings.Index(trimmed, ":")
		if colonIdx < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:colonIdx])
		stem := extractStem(key)
		if stem == "" {
			continue
		}
		image, ok := r.tagMappings[stem]
		if !ok {
			continue
		}
		rest := strings.TrimSpace(trimmed[colonIdx+1:])
		version := strings.Trim(rest, `'"`)
		// Skip: CI variable interpolation, already a digest, image:tag format, empty
		if version == "" || strings.HasPrefix(version, "$") || isSHA(version) || strings.Contains(version, ":") {
			continue
		}
		digest, err := r.docker.fetchDigest(image, version)
		if err != nil {
			fmt.Printf("  warn: GitLab input %s (%s:%s): %v\n", key, image, version, err)
			continue
		}
		indent := line[:len(line)-len(trimmed)]
		// Preserve original quote style
		quote := ""
		if strings.HasPrefix(rest, `"`) {
			quote = `"`
		} else if strings.HasPrefix(rest, `'`) {
			quote = `'`
		}
		lines[i] = fmt.Sprintf("%s%s: %s%s%s # %s", indent, key, quote, digest, quote, version)
	}
	return strings.Join(lines, "\n")
}

// Resolve replaces image tags and bare version inputs to digests.
func (r *gitlabResolver) Resolve(content string, pinActions, pinImages bool) (string, error) {
	if !pinImages {
		return content, nil
	}
	result := r.docker.resolveImages(content)
	result = r.resolveComponentInputs(result)
	result = r.resolveMappedVersionInputs(result)
	return result, nil
}
