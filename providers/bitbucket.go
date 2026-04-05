package providers

const bitbucketPipelinesBase = "bitbucket-pipelines"

func NewBitbucketResolver() *imageOnlyResolver {
	return &imageOnlyResolver{
		providerName: "Bitbucket Pipelines",
		matcher: func(p string) bool {
			return p == bitbucketPipelinesBase+".yml" || p == bitbucketPipelinesBase+".yaml"
		},
		docker: newDockerResolver(""),
	}
}
