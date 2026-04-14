package providers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
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
func (d *dockerResolver) resolveImages(content string) string {
	d.warnIfDrifted(content)
	return dockerImageRegex.ReplaceAllStringFunc(content, d.pinImage)
}

// warnIfDrifted checks already-pinned image digests and warns if the tag now
// resolves to a different digest. The file is never modified.
func (d *dockerResolver) warnIfDrifted(content string) {
	(&driftChecker{
		pinnedRegex: dockerPinnedRegex,
		kind:        "image",
		resolve:     d.fetchDigest,
	}).checkAll(content)
}

func (d *dockerResolver) pinImage(match string) string {
	parts := dockerImageRegex.FindStringSubmatch(match)
	if len(parts) < 6 {
		return match
	}
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

	if isSHA(tag) || tag == "latest" {
		return match
	}
	digest, err := d.cache.getOrSet(strippedImage+":"+tag, func() (string, error) {
		return d.fetchDigest(strippedImage, tag)
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "  warn: docker image %s:%s: %v\n", strippedImage, tag, err)
		return match
	}
	// Preserve the dependency proxy variable prefix in the output so the
	// pipeline continues to pull through the proxy.
	outputImage := strippedImage
	if isProxy {
		outputImage = proxyVar + strippedImage
	}
	return fmt.Sprintf("%s%s@%s%s # %s", prefix, outputImage, digest, suffix, tag)
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
