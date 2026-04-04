package scanner

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// dockerImageRegex is shared between GitHub and GitLab scanners.
// Matches `image: registry/name:tag` (with optional quotes).
// Does not match digest refs (image@sha256:...) — those are already pinned.
var dockerImageRegex = mustCompile(`(image:\s+['"]?)([a-zA-Z0-9_.\-/]+):([a-zA-Z0-9_.\-]+)(['"]?)`)

// dockerPinnedRegex matches already-pinned `image: name@sha256:digest # tag`.
var dockerPinnedRegex = mustCompile(`image:\s+['"]?([a-zA-Z0-9_.\-/]+)@(sha256:[0-9a-f]+)['"]?\s+#\s+(\S+)`)

type dockerResolver struct {
	client *http.Client
	token  string // optional registry token (e.g. GitLab)
	mu     sync.Mutex
	cache  map[string]string
}

func newDockerResolver(registryToken string) *dockerResolver {
	return &dockerResolver{
		client: &http.Client{},
		token:  registryToken,
		cache:  make(map[string]string),
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
	for _, parts := range dockerPinnedRegex.FindAllStringSubmatch(content, -1) {
		image, pinnedDigest, tag := parts[1], parts[2], parts[3]
		currentDigest, err := d.fetchDigest(image, tag)
		if err != nil {
			continue
		}
		if currentDigest != pinnedDigest {
			fmt.Printf("%s%sWARNING: %s:%s digest has drifted — image was mutated!%s\n  pinned: %s\n  current: %s\n  → update this ref manually\n",
				colorBold, colorYellow, image, tag, colorReset, pinnedDigest, currentDigest)
		}
	}
}

func (d *dockerResolver) pinImage(match string) string {
	parts := dockerImageRegex.FindStringSubmatch(match)
	if len(parts) < 5 {
		return match
	}
	prefix, image, tag, suffix := parts[1], parts[2], parts[3], parts[4]
	if isSHA(tag) || tag == "latest" {
		return match
	}
	cacheKey := image + ":" + tag
	d.mu.Lock()
	digest, ok := d.cache[cacheKey]
	d.mu.Unlock()
	if !ok {
		var err error
		digest, err = d.fetchDigest(image, tag)
		if err != nil {
			fmt.Printf("  warn: docker image %s:%s: %v\n", image, tag, err)
			return match
		}
		d.mu.Lock()
		d.cache[cacheKey] = digest
		d.mu.Unlock()
	}
	return fmt.Sprintf("%s%s@%s%s # %s", prefix, image, digest, suffix, tag)
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

	resp, err := doWithRetry(d.client, req, 3)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return "", fmt.Errorf("no Docker-Content-Digest header in response")
	}
	return digest, nil
}

// fetchAuthToken gets an anonymous Bearer token from the registry's auth service.
func (d *dockerResolver) fetchAuthToken(registryHost, repoPath string) (string, error) {
	// Determine auth URL based on registry
	var authURL string
	switch registryHost {
	case "registry-1.docker.io":
		authURL = fmt.Sprintf("https://auth.docker.io/token?service=registry.docker.io&scope=repository:%s:pull", repoPath)
	case "ghcr.io":
		authURL = fmt.Sprintf("https://ghcr.io/token?scope=repository:%s:pull", repoPath)
	default:
		// Try standard OAuth2 token endpoint (works for most OCI registries)
		authURL = fmt.Sprintf("https://%s/v2/token?scope=repository:%s:pull&service=%s", registryHost, repoPath, registryHost)
	}

	authReq, err := http.NewRequest("GET", authURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := doWithRetry(d.client, authReq, 3)
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
		return "", nil // non-fatal, try without token
	}
	if result.Token != "" {
		return result.Token, nil
	}
	return result.AccessToken, nil
}
