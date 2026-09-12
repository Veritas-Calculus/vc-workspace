package pve

import "regexp"

var configurationDigestPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// ValidConfigurationDigest accepts PVE's configuration SHA1, not an identity
// proof. An absent precondition must never become an unconditional GPU write.
func ValidConfigurationDigest(digest string) bool {
	return configurationDigestPattern.MatchString(digest)
}
