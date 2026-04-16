package providers

import (
	"fmt"
	"net/http"
	"strings"
)

var (
	dockerfileFromRegex  = mustCompile(patternFromLine)
	dockerfileFromPinned = mustCompile(patternFromPinned)
)

type dockerfileResolver struct {
	docker *dockerResolver
}

// NewDockerfileResolver returns a provider that pins FROM lines in Dockerfiles.
func NewDockerfileResolver() *dockerfileResolver {
	return &dockerfileResolver{docker: newDockerResolver("")}
}

func (r *dockerfileResolver) Name() string { return "Dockerfile" }

func (r *dockerfileResolver) IsMatch(relPath string) bool {
	base := slashBase(relPath)
	// Dockerfile, Dockerfile.prod, service.dockerfile, etc.
	return base == "Dockerfile" ||
		strings.HasPrefix(base, "Dockerfile.") ||
		strings.HasSuffix(base, ".dockerfile") ||
		strings.HasSuffix(base, ".Dockerfile")
}

func (r *dockerfileResolver) Resolve(content string, _, pinImages bool) (string, []string, error) {
	if !pinImages {
		return content, nil, nil
	}
	r.warnIfDrifted(content)
	var warns []string
	return r.pinFrom(content, &warns), warns, nil
}

func (r *dockerfileResolver) warnIfDrifted(content string) {
	// patternFromPinned captures (image, tag, sha) — different order from the
	// generic driftChecker convention — so we handle it inline.
	for _, parts := range dockerfileFromPinned.FindAllStringSubmatch(content, -1) {
		image, tag, pinnedSHA := parts[1], parts[2], parts[3]
		currentSHA, err := r.docker.fetchDigest(image, tag)
		if err != nil {
			continue
		}
		if currentSHA != pinnedSHA {
			warnDrift("image", image, tag, pinnedSHA, currentSHA)
		}
	}
}

func (r *dockerfileResolver) pinFrom(content string, warns *[]string) string {
	return dockerfileFromRegex.ReplaceAllStringFunc(content, func(match string) string {
		parts := dockerfileFromRegex.FindStringSubmatch(match)
		if len(parts) < 5 {
			return match
		}
		prefix, image, tag, trailing := parts[1], parts[2], parts[3], parts[4]

		if isSHA(tag) || tag == "latest" {
			return match
		}
		digest, err := r.docker.cache.getOrSet(image+":"+tag, func() (string, error) {
			return r.docker.fetchDigest(image, tag)
		})
		if err != nil {
			*warns = append(*warns, fmt.Sprintf("Dockerfile FROM %s:%s: %v", image, tag, err))
			return match
		}
		// trailing is either " " (before AS alias) or "\n" (end of line).
		// Preserve it exactly so the remainder of the line (e.g. "AS builder") stays intact.
		// The image:tag comment goes on the line above to keep the FROM line valid.
		return fmt.Sprintf("# %s:%s\n%s%s@%s%s", image, tag, prefix, image, digest, trailing)
	})
}

// setClient allows tests to inject a fake HTTP client.
func (r *dockerfileResolver) setClient(c *http.Client) { r.docker.client = c }
