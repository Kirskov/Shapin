package providers

func NewCircleCIResolver(registryToken string) *imageOnlyResolver {
	return &imageOnlyResolver{
		providerName: "CircleCI",
		matcher: func(p string) bool {
			return matchesAny(p,
				".circleci/config.yml",
				".circleci/config.yaml",
			)
		},
		docker: newDockerResolver(registryToken),
	}
}
