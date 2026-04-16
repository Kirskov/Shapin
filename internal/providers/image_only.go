package providers

import "net/http"

// imageOnlyResolver is a generic provider that pins Docker image: tags only.
// It is used by CI systems whose native action/pipe formats have no SHA pinning
// API (CircleCI orbs, Bitbucket Pipes).
type imageOnlyResolver struct {
	providerName string
	matcher      func(string) bool
	docker       *dockerResolver
}

func (r *imageOnlyResolver) Name() string { return r.providerName }

func (r *imageOnlyResolver) IsMatch(relPath string) bool { return r.matcher(relPath) }

func (r *imageOnlyResolver) Resolve(content string, _, pinImages bool) (string, []string, error) {
	if !pinImages {
		return content, nil, nil
	}
	var warns []string
	return r.docker.resolveImages(content, &warns), warns, nil
}

// setClient allows tests to inject a fake HTTP client into the docker resolver.
func (r *imageOnlyResolver) setClient(c *http.Client) {
	r.docker.client = c
}
