package plugin

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

func newGrafanaHTTPClient(transport http.RoundTripper) *http.Client {
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme == "http" {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
}

func grafanaTokenForURL(rawURL, token string) string {
	if token == "" {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "https" {
		return token
	}
	if u.Scheme != "http" {
		return ""
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return token
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return token
	}
	return ""
}

func applyGrafanaAuth(req *http.Request, token string) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}
