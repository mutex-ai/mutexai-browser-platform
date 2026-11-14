package applications

import (
	"fmt"
	"net"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

var dnsNames = idna.New(idna.MapForLookup(), idna.StrictDomainName(true), idna.VerifyDNSLength(true))

// normalizeHost accepts bare DNS hosts/patterns, never URLs, ports or IPs.
// Exact resolver hosts omit www because CP already treats both forms as aliases.
// Wildcards remain literal patterns; each * keeps CP's single-label semantics.
func normalizeHost(raw string, wildcard, alias bool) (string, error) {
	host := strings.ToLower(strings.TrimSpace(raw))
	if host == "" || strings.ContainsAny(host, `/\:@?#%[]`) {
		return "", ErrInvalid
	}
	labels := strings.Split(host, ".")
	for i, label := range labels {
		if strings.Contains(label, "*") {
			if !wildcard || strings.ContainsFunc(label, func(c rune) bool { return c > unicode.MaxASCII }) {
				return "", ErrInvalid
			}
		} else {
			ascii, err := idna.Lookup.ToASCII(label)
			if err != nil {
				return "", ErrInvalid
			}
			labels[i] = ascii
		}
	}
	host = strings.Join(labels, ".")
	if strings.HasPrefix(host, ".") || strings.Contains(host, "..") {
		return "", ErrInvalid
	}
	// IDNA validates DNS syntax and lengths. Replacing stars only for validation
	// leaves wildcard patterns intact for CP's existing matcher.
	if _, err := dnsNames.ToASCII(strings.ReplaceAll(host, "*", "x")); err != nil {
		return "", ErrInvalid
	}
	host = strings.TrimSuffix(host, ".")
	if !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return "", ErrInvalid
	}
	if alias && !strings.Contains(host, "*") && strings.HasPrefix(host, "www.") {
		// Strip only subdomain aliases, preserving registrable names like www.com.
		if root, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
			for host != root && strings.HasPrefix(host, "www.") {
				host = strings.TrimPrefix(host, "www.")
			}
		}
	}
	return host, nil
}

func normalizeHosts(object map[string]any, field string, wildcard, alias bool) ([]string, error) {
	values, ok := object[field].([]any)
	if !ok { // The schema reports missing fields and incorrect types.
		return nil, nil
	}
	hosts := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		raw, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%w: invalid %s host", ErrInvalid, field)
		}
		host, err := normalizeHost(raw, wildcard, alias)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid hostname in profile.urls.%s", ErrInvalid, field)
		}
		if !seen[host] {
			hosts = append(hosts, host)
			seen[host] = true
		}
	}
	if alias { // Homepage order carries the preferred display host.
		slices.Sort(hosts)
	}
	object[field] = stringValues(hosts)
	return hosts, nil
}
