package scanner

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// dockerImageRegex is shared between GitHub and GitLab scanners.
// Matches `image: registry/name:tag` (with optional quotes).
// Does not match digest refs (image@sha256:...) — those are already pinned.
var dockerImageRegex = mustCompile(`(image:\s+['"]?)([a-zA-Z0-9_.\-/]+):([a-zA-Z0-9_.\-]+)(['"]?)`)

type dockerResolver struct {
	client *http.Client
	token  string // optional registry token (e.g. GitLab)
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
	return dockerImageRegex.ReplaceAllStringFunc(content, func(match string) string {
		parts := dockerImageRegex.FindStringSubmatch(match)
		if len(parts) < 5 {
			return match
		}
		prefix := parts[1] // "image: "
		image := parts[2]  // "maildev/maildev"
		tag := parts[3]    // "2.2.1"
		suffix := parts[4] // closing quote if any

		// Already a digest or "latest" — skip
		if isSHA(tag) || tag == "latest" {
			return match
		}

		cacheKey := image + ":" + tag
		digest, ok := d.cache[cacheKey]
		if !ok {
			var err error
			digest, err = d.fetchDigest(image, tag)
			if err != nil {
				fmt.Printf("  warn: docker image %s:%s: %v\n", image, tag, err)
				return match
			}
			d.cache[cacheKey] = digest
		}

		return fmt.Sprintf("%s%s@%s%s # %s", prefix, image, digest, suffix, tag)
	})
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

	resp, err := d.client.Do(req)
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

	resp, err := d.client.Get(authURL)
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
