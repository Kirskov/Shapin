package scanner

import "regexp"

var shaRegex = regexp.MustCompile(`^[0-9a-f]{40}$`)

// isSHA returns true if the string looks like a full git SHA or docker digest.
func isSHA(s string) bool {
	return shaRegex.MatchString(s) || len(s) > 7 && s[:7] == "sha256:"
}

const bearerPrefix = "Bearer "

func mustCompile(pattern string) *regexp.Regexp {
	return regexp.MustCompile(pattern)
}

