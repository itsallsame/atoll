package model

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const SourceKeyNormalizationVersion = "source-url-v1"

// CanonicalHTTPURL provides deterministic identity normalization; it never
// performs DNS or an HTTP request. An empty URL is allowed for a Company whose
// official website is not known yet.
func CanonicalHTTPURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("absolute http(s) URL required")
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || u.User != nil {
		return "", fmt.Errorf("URL host is required and userinfo is forbidden")
	}
	// A colon in Hostname is only valid for an IPv6 literal. net/url accepts
	// ambiguous inputs such as http://:0:1 and reports ":0" as the hostname;
	// feeding that value to JoinHostPort would create a canonical URL that the
	// parser itself cannot read on the next pass.
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return "", fmt.Errorf("URL host is invalid")
	}
	if strings.HasSuffix(u.Host, ":") || strings.HasSuffix(host, ":") {
		return "", fmt.Errorf("URL port is invalid")
	}
	port := u.Port()
	if port != "" {
		portNumber, err := strconv.ParseUint(port, 10, 16)
		if err != nil || portNumber == 0 {
			return "", fmt.Errorf("URL port is invalid")
		}
	}
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	}
	u.Host = host
	u.Fragment = ""
	if u.Path == "/" {
		u.Path = ""
	} else {
		u.Path = strings.TrimRight(u.Path, "/")
	}
	query := u.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "utm_") || lower == "gclid" || lower == "fbclid" {
			query.Del(key)
		}
	}
	for key, values := range query {
		sort.Strings(values)
		query[key] = values
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func CanonicalSourceKey(endpoint, category string) (string, error) {
	canonical, err := CanonicalHTTPURL(endpoint)
	if err != nil {
		return "", err
	}
	if canonical == "" {
		return "", fmt.Errorf("source endpoint is required")
	}
	category = strings.ToLower(strings.TrimSpace(category))
	categoryKey := category
	for _, value := range []byte(category) {
		if value >= 0x80 {
			// Storage identity columns are deliberately ASCII. Keep common
			// categories byte-for-byte compatible while encoding localized
			// special-program names without discarding their identity.
			categoryKey = "utf8hex:" + hex.EncodeToString([]byte(category))
			break
		}
	}
	return SourceKeyNormalizationVersion + "|" + canonical + "|" + categoryKey, nil
}
