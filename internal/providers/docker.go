package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

const (
	dockerHubRegistry   = "registry-1.docker.io"
	dockerHubAuthURL    = "https://auth.docker.io/token?service=registry.docker.io&scope=repository:%s:pull"
	ghcrAuthURL         = "https://ghcr.io/token?scope=repository:%s:pull"
	genericAuthURL      = "https://%s/v2/token?scope=repository:%s:pull&service=%s"
	dockerContentDigest = "Docker-Content-Digest"
)

// dockerImageRegex is shared between GitHub and GitLab scanners.
// Matches `image: registry/name:tag` (with optional quotes).
// Does not match digest refs (image@sha256:...) — those are already pinned.
var dockerImageRegex = mustCompile(patternDockerImage)

// dockerNameRegex matches `name: registry/name:tag` — used by GitLab CI's
// extended image syntax where `image:` is a mapping with a `name:` sub-key.
var dockerNameRegex = mustCompile(patternDockerName)

// dockerServiceRegex matches bare list items like `  - postgres:15` inside
// GitLab CI services: blocks.
var dockerServiceRegex = mustCompile(patternGLService)

// dockerPinnedRegex matches already-pinned `image: name@sha256:digest # tag`.
var dockerPinnedRegex = mustCompile(patternDockerPinned)

type dockerResolver struct {
	client *http.Client
	token  string // optional registry token (e.g. GitLab)
	cache  syncCache
}

func newDockerResolver(registryToken string) *dockerResolver {
	return &dockerResolver{
		client: newHTTPClient(),
		token:  registryToken,
		cache:  newSyncCache(),
	}
}

// resolveImages replaces `image: name:tag` with `image: name@sha256:xxx # tag`
// in the given content. Non-fatal: leaves unresolvable images untouched.
func (d *dockerResolver) resolveImages(content string, warns *[]string) string {
	dc := &driftChecker{
		pinnedRegex: dockerPinnedRegex,
		kind:        "image",
		resolve:     d.fetchDigest,
	}
	dc.checkAll(content, warns)
	content = dc.fixDrift(content)
	return d.pinImageRefs(dockerImageRegex, content, warns)
}

// resolveImageNames replaces `name: name:tag` with `name: name@sha256:xxx # tag`
// for GitLab CI's extended image syntax (image: {name: ..., entrypoint: ...}).
func (d *dockerResolver) resolveImageNames(content string, warns *[]string) string {
	return d.pinImageRefs(dockerNameRegex, content, warns)
}

// resolveServices pins bare list items in GitLab CI services: blocks,
// e.g. `  - postgres:15` → `  - postgres@sha256:... # 15`.
// It only processes lines that appear after a `services:` key and stops at
// the next top-level key (no leading spaces).
func (d *dockerResolver) resolveServices(content string, warns *[]string) string {
	lines := strings.Split(content, "\n")
	inServices := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Detect `services:` key at any indent level.
		if strings.HasSuffix(strings.TrimRight(trimmed, " \t"), "services:") || trimmed == "services:" {
			inServices = true
			continue
		}
		// A non-empty, non-comment line with no leading space ends the services block.
		if inServices && len(line) > 0 && line[0] != ' ' && line[0] != '\t' && line[0] != '#' && !strings.HasPrefix(trimmed, "-") {
			inServices = false
		}
		if !inServices {
			continue
		}
		// Only process bare list items: `  - image:tag`
		if !dockerServiceRegex.MatchString(line) {
			continue
		}
		lines[i] = dockerServiceRegex.ReplaceAllStringFunc(line, func(match string) string {
			parts := dockerServiceRegex.FindStringSubmatch(match)
			if len(parts) < 6 {
				return match
			}
			return d.pinImageParts(match, parts, warns)
		})
	}
	return strings.Join(lines, "\n")
}

// pinImageRefs pins all image references matched by re in content.
func (d *dockerResolver) pinImageRefs(re *regexp.Regexp, content string, warns *[]string) string {
	return re.ReplaceAllStringFunc(content, func(match string) string {
		parts := re.FindStringSubmatch(match)
		if len(parts) < 6 {
			return match
		}
		return d.pinImageParts(match, parts, warns)
	})
}


func (d *dockerResolver) pinImageParts(match string, parts []string, warns *[]string) string {
	// parts[1]=prefix (e.g. "  image: "), parts[2]=optional proxy var prefix,
	// parts[3]=image name, parts[4]=tag, parts[5]=closing quote
	prefix, proxyVar, image, tag, suffix := parts[1], parts[2], parts[3], parts[4], parts[5]

	// proxyVar already contains the trailing "/", so strip it before checking.
	// Only known dependency proxy variables are stripped; other $VAR/ prefixes are kept.
	proxyVarName := strings.TrimSuffix(proxyVar, "/")
	strippedImage := image
	isProxy := false
	if proxyVarName != "" {
		if _, ok := stripDependencyProxyPrefix(proxyVarName + "/" + image); ok {
			strippedImage = image
			isProxy = true
		}
	}
	if !isProxy {
		// Unknown variable prefix — include it in the image name as-is.
		strippedImage = proxyVar + image
	}

	if isSHA(tag) {
		return match
	}
	if tag == "latest" {
		*warns = append(*warns, fmt.Sprintf("docker image %s:%s: avoid 'latest' — pin to an explicit tag", strippedImage, tag))
		return match
	}
	digest, err := d.cache.getOrSet(strippedImage+":"+tag, func() (string, error) {
		return d.fetchDigest(strippedImage, tag)
	})
	if err != nil {
		*warns = append(*warns, fmt.Sprintf("docker image %s:%s: %v", strippedImage, tag, err))
		return match
	}
	// Preserve the dependency proxy variable prefix in the output so the
	// pipeline continues to pull through the proxy.
	outputImage := strippedImage
	if isProxy {
		outputImage = proxyVar + strippedImage
	}
	return fmt.Sprintf("%s%s@%s%s # %s:%s", prefix, outputImage, digest, suffix, strippedImage, tag)
}

// fetchDigest fetches the docker content digest for image:tag.
func (d *dockerResolver) fetchDigest(image, tag string) (string, error) {
	registryHost, repoPath := splitRegistryAndRepo(image)

	// Fetch an auth token for this registry/repo
	token, err := d.fetchAuthToken(registryHost, repoPath)
	if err != nil {
		return "", fmt.Errorf("auth: %w", err)
	}

	manifestURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registryHost, repoPath, tag)
	req, err := http.NewRequest("GET", manifestURL, nil)
	if err != nil {
		return "", err
	}
	// Request the most specific manifest type to get a stable digest
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	req.Header.Add("Accept", "application/vnd.oci.image.manifest.v1+json")
	if token != "" {
		req.Header.Set("Authorization", bearerPrefix+token)
	} else if d.token != "" {
		req.Header.Set("Authorization", bearerPrefix+d.token)
	}

	resp, err := doWithRetry(d.client, req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	digest := resp.Header.Get(dockerContentDigest)
	if digest == "" {
		return "", fmt.Errorf("no %s header in response", dockerContentDigest)
	}
	return digest, nil
}

// fetchAuthToken gets an anonymous Bearer token from the registry's auth service.
func (d *dockerResolver) fetchAuthToken(registryHost, repoPath string) (string, error) {
	// Determine auth URL based on registry
	var authURL string
	switch registryHost {
	case dockerHubRegistry:
		authURL = fmt.Sprintf(dockerHubAuthURL, repoPath)
	case "ghcr.io":
		authURL = fmt.Sprintf(ghcrAuthURL, repoPath)
	default:
		// Try standard OAuth2 token endpoint (works for most OCI registries)
		authURL = fmt.Sprintf(genericAuthURL, registryHost, repoPath, registryHost)
	}

	authReq, err := http.NewRequest("GET", authURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := doWithRetry(d.client, authReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// Some registries return 401 with Www-Authenticate instead — skip token fetch
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusNotFound {
		return "", nil
	}

	var result struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		// Non-fatal: registry may not use a token-based auth flow.
		// Proceed without a token and let the manifest request fail if needed.
		return "", nil
	}
	if result.Token != "" {
		return result.Token, nil
	}
	return result.AccessToken, nil
}

// splitRegistryAndRepo splits "registry.gitlab.com/group/image" into
// ("registry.gitlab.com", "group/image"). Defaults to Docker Hub.
func splitRegistryAndRepo(image string) (registryHost, repoPath string) {
	parts := strings.SplitN(image, "/", 2)
	if len(parts) == 2 && (strings.Contains(parts[0], ".") || strings.Contains(parts[0], ":")) {
		return parts[0], parts[1]
	}
	if !strings.Contains(image, "/") {
		return dockerHubRegistry, "library/" + image
	}
	return dockerHubRegistry, image
}
