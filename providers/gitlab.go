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
	tagMappings map[string]string // key (e.g. NODE_TAG) → image (e.g. node)
}

func NewGitLabResolver(host, token string, tagMappings map[string]string) *gitlabResolver {
	return &gitlabResolver{
		docker:      newDockerResolver(token),
		tagMappings: tagMappings,
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
// Also handles plain version values (e.g. NODE_TAG: '24.13.1') via tagMappings.
func (r *gitlabResolver) resolveComponentInputs(content string) string {
	tagKeys := collectTagInputKeys(content, r.tagMappings)
	result := content
	if len(tagKeys) > 0 {
		// Pin standard image:tag values
		result = gitlabInputTagRegex.ReplaceAllStringFunc(result, r.pinInputTagMatch(tagKeys))
	}
	// Pin plain version values via tagMappings (runs independently)
	if len(r.tagMappings) > 0 {
		result = r.resolveMappedTagInputs(result)
	}
	return result
}

// collectTagInputKeys parses the YAML and returns the set of input key names
// that contain "TAG" and hold an unpinned image:tag value, or a plain version
// value for keys present in tagMappings.
// It walks the entire document recursively so it catches variables: and inputs:
// at any nesting level (top-level, inside jobs, inside include blocks, etc.).
func collectTagInputKeys(content string, tagMappings map[string]string) map[string]bool {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil || root.Kind == 0 {
		return nil
	}
	keys := make(map[string]bool)
	walkTagMaps(&root, keys, tagMappings)
	return keys
}

// walkTagMaps recursively walks a yaml.Node tree and collects keys that
// contain "TAG" from any "variables" or "inputs" mapping node.
func walkTagMaps(node *yaml.Node, keys map[string]bool, tagMappings map[string]string) {
	if node == nil {
		return
	}
	if node.Kind == yaml.DocumentNode {
		for _, child := range node.Content {
			walkTagMaps(child, keys, tagMappings)
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
				collectTagKeysFromMap(valNode, keys, tagMappings)
			}
			// Recurse into all values regardless
			walkTagMaps(valNode, keys, tagMappings)
		}
		return
	}
	if node.Kind == yaml.SequenceNode {
		for _, child := range node.Content {
			walkTagMaps(child, keys, tagMappings)
		}
	}
}

// collectTagKeysFromMap adds keys containing "TAG" with an image:tag value
// from a mapping node into keys. Also adds keys present in tagMappings with
// a plain version value (e.g. NODE_TAG: '24.13.1').
func collectTagKeysFromMap(node *yaml.Node, keys map[string]bool, tagMappings map[string]string) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		k := node.Content[i].Value
		v := node.Content[i+1].Value
		if isSHA(v) {
			continue
		}
		// Standard image:tag format
		if strings.Contains(strings.ToUpper(k), "TAG") && strings.Contains(v, ":") {
			keys[k] = true
			continue
		}
		// Plain version via tag mapping (e.g. NODE_TAG: '24.13.1')
		if _, ok := tagMappings[k]; ok {
			keys[k] = true
		}
	}
}

// resolveMappedTagInputs pins plain version values (e.g. NODE_TAG: '24.13.1')
// using the tagMappings to determine the image name.
func (r *gitlabResolver) resolveMappedTagInputs(content string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		colonIdx := strings.Index(trimmed, ":")
		if colonIdx < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:colonIdx])
		image, ok := r.tagMappings[key]
		if !ok {
			continue
		}
		rest := strings.TrimSpace(trimmed[colonIdx+1:])
		// Strip surrounding quotes
		tag := strings.Trim(rest, `'"`)
		if tag == "" || isSHA(tag) || strings.Contains(tag, ":") || strings.HasPrefix(tag, "$") {
			continue
		}
		digest, err := r.docker.fetchDigest(image, tag)
		if err != nil {
			fmt.Printf("  warn: GitLab input %s (%s:%s): %v\n", key, image, tag, err)
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		// Preserve original quote style
		quote := ""
		if strings.HasPrefix(rest, `"`) {
			quote = `"`
		} else if strings.HasPrefix(rest, `'`) {
			quote = `'`
		}
		// Rename key: replace trailing _TAG or _VERSION suffix with _DIGEST
		newKey := key
		for _, suffix := range []string{"_TAG", "_VERSION"} {
			if strings.HasSuffix(newKey, suffix) {
				newKey = newKey[:len(newKey)-len(suffix)] + "_DIGEST"
				break
			}
		}
		lines[i] = fmt.Sprintf("%s%s: %s%s@%s%s # %s", indent, newKey, quote, image, digest, quote, tag)
	}
	return strings.Join(lines, "\n")
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

// Resolve replaces image tags to digests.
func (r *gitlabResolver) Resolve(content string, pinActions, pinImages bool) (string, error) {
	if !pinImages {
		return content, nil
	}
	result := r.docker.resolveImages(content)
	result = r.resolveComponentInputs(result)
	return result, nil
}
