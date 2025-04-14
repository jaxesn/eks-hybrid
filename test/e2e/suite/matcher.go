package suite

import "github.com/aws/eks-hybrid/internal/creds"

type NodeadmConfigMatcher struct {
	MatchOS            func(name string) bool
	MatchCredsProvider func(name creds.CredentialProvider) bool
}

func (m NodeadmConfigMatcher) matches(osName string, creds creds.CredentialProvider) bool {
	return m.MatchOS(osName) && m.MatchCredsProvider(creds)
}

type NodeadmConfigMatchers []NodeadmConfigMatcher

func (m NodeadmConfigMatchers) Matches(osName string, creds creds.CredentialProvider) bool {
	for _, matcher := range m {
		if matcher.matches(osName, creds) {
			return true
		}
	}
	return false
}
