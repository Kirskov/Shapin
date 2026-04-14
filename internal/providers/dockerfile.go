package providers

import (
	"fmt"
	"net/http"
	"os"
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

func (r *dockerfileResolver) Resolve(content string, _, pinImages bool) (string, error) {
	if !pinImages {
		return content, nil
	}
	r.warnIfDrifted(content)
	return r.pinFrom(content), nil
}

func (r *dockerfileResolver) warnIfDrifted(content string) {
	(&driftChecker{
		pinnedRegex: dockerfileFromPinned,
		kind:        "image",
		resolve:     r.docker.fetchDigest,
	}).checkAll(content)
}

func (r *dockerfileResolver) pinFrom(content string) string {
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
			fmt.Fprintf(os.Stderr, "  warn: Dockerfile FROM %s:%s: %v\n", image, tag, err)
			return match
		}
		// trailing is either " " (before AS alias) or "\n" (end of line).
		// Preserve it exactly so the remainder of the line (e.g. "AS builder") stays intact.
		return fmt.Sprintf("%s%s@%s # %s:%s%s", prefix, image, digest, image, tag, trailing)
	})
}

// setClient allows tests to inject a fake HTTP client.
func (r *dockerfileResolver) setClient(c *http.Client) { r.docker.client = c }
