package providers

import (
	"fmt"
	"net/http"
	"strings"
)

var (
	tfvarsImageRegex  = mustCompile(patternTFVarsImage)
	tfvarsPinnedRegex = mustCompile(patternTFVarsPinned)
)

type terraformResolver struct {
	docker *dockerResolver
}

// NewTerraformResolver returns a provider that pins Docker image literals in
// .tfvars files. Only literal string values are handled — variable references
// (var.foo) are left untouched.
func NewTerraformResolver() *terraformResolver {
	return &terraformResolver{docker: newDockerResolver("")}
}

func (r *terraformResolver) Name() string { return "Terraform" }

func (r *terraformResolver) IsMatch(relPath string) bool {
	base := slashBase(relPath)
	return strings.HasSuffix(base, ".tfvars")
}

func (r *terraformResolver) Resolve(content string, _, pinImages bool) (string, []string, error) {
	if !pinImages {
		return content, nil, nil
	}
	var warns []string
	content = r.fixAndWarnDrifted(content, &warns)
	return r.pinImages(content, &warns), warns, nil
}

// fixAndWarnDrifted updates drifted pinned image digests to their current digest
// and appends warnings for each one found.
// patternTFVarsPinned captures: (prefix, image, pinnedDigest, tag, closingQuote)
func (r *terraformResolver) fixAndWarnDrifted(content string, warns *[]string) string {
	return tfvarsPinnedRegex.ReplaceAllStringFunc(content, func(match string) string {
		parts := tfvarsPinnedRegex.FindStringSubmatch(match)
		if len(parts) < 6 {
			return match
		}
		prefix, image, pinnedDigest, tag, closing := parts[1], parts[2], parts[3], parts[4], parts[5]
		currentDigest, err := r.docker.fetchDigest(image, tag)
		if err != nil {
			return match
		}
		if currentDigest == pinnedDigest {
			return match
		}
		warnDrift("image", image, tag, pinnedDigest, currentDigest, warns)
		return fmt.Sprintf("%s%s@%s # %s:%s%s", prefix, image, currentDigest, image, tag, closing)
	})
}

func (r *terraformResolver) pinImages(content string, warns *[]string) string {
	return tfvarsImageRegex.ReplaceAllStringFunc(content, func(match string) string {
		parts := tfvarsImageRegex.FindStringSubmatch(match)
		if len(parts) < 5 {
			return match
		}
		prefix, image, tag, suffix := parts[1], parts[2], parts[3], parts[4]
		if isSHA(tag) {
			return match
		}
		if tag == "latest" {
			*warns = append(*warns, fmt.Sprintf("docker image %s:%s: avoid 'latest' — pin to an explicit tag", image, tag))
			return match
		}
		digest, err := r.docker.cache.getOrSet(image+":"+tag, func() (string, error) {
			return r.docker.fetchDigest(image, tag)
		})
		if err != nil {
			*warns = append(*warns, fmt.Sprintf("docker image %s:%s: %v", image, tag, err))
			return match
		}
		return fmt.Sprintf("%s%s@%s # %s:%s%s", prefix, image, digest, image, tag, suffix)
	})
}

// setClient allows tests to inject a fake HTTP client.
func (r *terraformResolver) setClient(c *http.Client) { r.docker.client = c }
