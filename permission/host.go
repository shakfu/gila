package permission

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// normalHost lowercases a host and drops a port and a trailing dot, so "API.Example.com.:443"
// and "api.example.com" compare equal.
func normalHost(h string) string {
	if host, port, err := net.SplitHostPort(h); err == nil && port != "" && strings.Trim(port, "0123456789") == "" {
		h = host
	}
	return strings.TrimSuffix(strings.ToLower(strings.Trim(h, "[]")), ".")
}

// hostAllowed reports whether an entry allows host: an exact match, or "*.domain" for any
// host below domain but not domain itself.
func hostAllowed(entries []string, host string) bool {
	h := normalHost(host)
	if h == "" {
		return false
	}
	for _, e := range entries {
		e = normalHost(e)
		if domain, ok := strings.CutPrefix(e, "*."); ok {
			if strings.HasSuffix(h, "."+domain) {
				return true
			}
		} else if h == e {
			return true
		}
	}
	return false
}

var hostEntry = regexp.MustCompile(`^(\*\.)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

func checkHost(e string) error {
	if strings.Contains(e, "/") {
		return fmt.Errorf("bad host %q: give the host alone, not a URL", e)
	}
	h := normalHost(e)
	if net.ParseIP(h) != nil || hostEntry.MatchString(h) {
		return nil
	}
	return fmt.Errorf("bad host %q: an entry is a host such as \"pkg.go.dev\", or \"*.\" and a domain", e)
}
