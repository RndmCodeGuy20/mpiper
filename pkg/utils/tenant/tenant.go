// Package tenant provides shared validation for tenant identifiers used as
// storage key prefixes. Keeping the slug rules in one place ensures the CLI
// that mints API keys and the service that derives storage paths agree on
// what counts as a valid tenant.
package tenant

import "regexp"

// slugRe matches the tenant identifiers we accept: lowercase alphanumerics,
// underscore, and hyphen, between 1 and 64 characters. Anchors are required
// so partial matches are rejected.
var slugRe = regexp.MustCompile("^[a-z0-9_-]{1,64}$")

// IsValidSlug reports whether s is a usable tenant identifier.
func IsValidSlug(s string) bool {
	return slugRe.MatchString(s)
}
