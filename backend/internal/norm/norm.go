package norm

import (
	"fmt"
	"net"
	"strings"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

var idnaProfile = idna.New(
	idna.MapForLookup(),
	idna.StrictDomainName(true),
	idna.Transitional(false),
)

// Apex validates and normalizes an apex (eTLD+1) input.
// Returns the normalized apex or an error with the exact message strings
// crt.name uses so the clone is byte-compatible.
func Apex(input string) (string, error) {
	if input == "" {
		return "", fmt.Errorf("missing apex parameter")
	}
	raw := strings.TrimSpace(input)
	if raw == "" {
		return "", fmt.Errorf("missing apex parameter")
	}
	// Lowercase and punycode via idna
	lower := strings.ToLower(raw)
	// Remove trailing dot
	lower = strings.TrimSuffix(lower, ".")
	ascii, err := idnaProfile.ToASCII(lower)
	if err != nil {
		// Surface as publicsuffix style error for compatibility
		return "", fmt.Errorf("invalid apex: publicsuffix: cannot derive eTLD+1 for domain %q", input)
	}
	// publicsuffix.EffectiveTLDPlusOne requires lowercased ascii
	etld1, err := publicsuffix.EffectiveTLDPlusOne(ascii)
	if err != nil {
		return "", fmt.Errorf("invalid apex: publicsuffix: cannot derive eTLD+1 for domain %q", input)
	}
	if etld1 != ascii {
		return "", fmt.Errorf("invalid apex: not an apex (eTLD+1 is %s)", etld1)
	}
	return ascii, nil
}

// Subdomain normalizes a single SAN hostname into (apex, sub).
// Returns ok=false if the name should be dropped (wildcard, IP, invalid).
func Subdomain(raw string) (apex, sub string, ok bool) {
	if raw == "" {
		return "", "", false
	}
	// Trim and lower
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", "", false
	}
	s = strings.ToLower(s)
	s = strings.TrimSuffix(s, ".")
	// Drop wildcard
	if strings.HasPrefix(s, "*.") {
		s = s[2:]
		if s == "" {
			return "", "", false
		}
	}
	if strings.Contains(s, "*") {
		return "", "", false
	}
	// Drop IPs
	if net.ParseIP(s) != nil {
		return "", "", false
	}
	// Must contain dot
	if !strings.Contains(s, ".") {
		return "", "", false
	}
	// idna
	ascii, err := idnaProfile.ToASCII(s)
	if err != nil {
		return "", "", false
	}
	// Basic label validation: no empty labels, no leading/trailing hyphens handled by idna already
	if strings.Contains(ascii, "..") || strings.HasPrefix(ascii, ".") || strings.HasSuffix(ascii, ".") {
		return "", "", false
	}
	// Derive apex
	etld1, err := publicsuffix.EffectiveTLDPlusOne(ascii)
	if err != nil {
		return "", "", false
	}
	return etld1, ascii, true
}

// MustApex is like Apex but panics on error (for tests/seed).
func MustApex(s string) string {
	a, err := Apex(s)
	if err != nil {
		panic(err)
	}
	return a
}
