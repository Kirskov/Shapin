package providers

import "strings"

// NewComposeResolver returns a provider that pins Docker image: tags in
// docker-compose and Compose spec files.
//
// Matched file names (at any directory depth):
//   - docker-compose.yml / docker-compose.yaml
//   - docker-compose.*.yml / docker-compose.*.yaml  (e.g. docker-compose.prod.yml)
//   - compose.yml / compose.yaml
func NewComposeResolver() *imageOnlyResolver {
	return &imageOnlyResolver{
		providerName: "Docker Compose",
		matcher: func(p string) bool {
			base := slashBase(p)
			return base == "docker-compose.yml" ||
				base == "docker-compose.yaml" ||
				base == "compose.yml" ||
				base == "compose.yaml" ||
				(strings.HasPrefix(base, "docker-compose.") && isYAML(base))
		},
		docker: newDockerResolver(""),
	}
}
