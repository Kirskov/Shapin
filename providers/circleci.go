package providers

const (
	circleciConfigYML  = ".circleci/config.yml"
	circleciConfigYAML = ".circleci/config.yaml"
)

func NewCircleCIResolver(registryToken string) *imageOnlyResolver {
	return &imageOnlyResolver{
		providerName: "CircleCI",
		matcher: func(p string) bool {
			return p == circleciConfigYML || p == circleciConfigYAML
		},
		docker: newDockerResolver(registryToken),
	}
}
