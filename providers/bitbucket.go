package providers

func NewBitbucketResolver() *imageOnlyResolver {
	return &imageOnlyResolver{
		providerName: "Bitbucket Pipelines",
		matcher: func(p string) bool {
			return matchesAny(slashBase(p),
				"bitbucket-pipelines.yml",
				"bitbucket-pipelines.yaml",
			)
		},
		docker: newDockerResolver(""),
	}
}
