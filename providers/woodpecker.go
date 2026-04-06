package providers

import "strings"

const (
	woodpeckerYML  = ".woodpecker.yml"
	woodpeckerYAML = ".woodpecker.yaml"
	woodpeckerDir  = ".woodpecker"
)

// NewWoodpeckerResolver returns a provider for Woodpecker CI config files:
//   - .woodpecker.yml / .woodpecker.yaml at the repo root
//   - any .yml/.yaml file inside .woodpecker/ (split pipeline config)
func NewWoodpeckerResolver() *imageOnlyResolver {
	return &imageOnlyResolver{
		providerName: "Woodpecker CI",
		matcher: func(p string) bool {
			if p == woodpeckerYML || p == woodpeckerYAML {
				return true
			}
			dir := slashDir(p)
			return (dir == woodpeckerDir || strings.HasPrefix(dir, woodpeckerDir+"/")) && isYAML(slashBase(p))
		},
		docker: newDockerResolver(""),
	}
}
