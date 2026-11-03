package proxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

func validDownloadHost(host string) bool {
	host = strings.TrimPrefix(host, "*.")
	if len(host) > 253 || !strings.Contains(host, ".") || net.ParseIP(host) != nil {
		return false
	}

	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}

		for _, c := range label {
			if !isLower(c) && !isDigit(c) && c != '-' {
				return false
			}
		}
	}

	return true
}

func (h *Handler) downloadAllowed(u *url.URL) bool {
	if u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}

	host := strings.ToLower(u.Hostname())
	for _, allowed := range h.downloadHosts {
		if host == allowed || strings.HasPrefix(allowed, "*.") && strings.HasSuffix(host, allowed[1:]) && host != allowed[2:] {
			return true
		}
	}

	return false
}

// download relays explicitly allowed object-storage redirects with a fresh
// GET request. Authentication, cookies, query parameters from the original
// request and all caller headers stay on the forge. Signed target queries
// are used only for the fetch and never returned to callers.
func (h *Handler) download(resp *http.Response) error {
	if len(h.downloadHosts) == 0 || resp.Request == nil || resp.Request.Method != http.MethodGet {
		return nil
	}

	for redirects := 0; redirects < 3; redirects++ {
		if !isDownloadRedirect(resp.StatusCode) {
			if redirects > 0 {
				cleanDownloadResponse(resp)
			}

			return nil
		}

		location := resp.Header.Values("Location")
		if len(location) != 1 {
			return nil
		}

		u, err := resp.Request.URL.Parse(location[0])
		if err != nil || !h.downloadAllowed(u) {
			if redirects > 0 {
				return errors.New("proxy: blocked download redirect")
			}

			return nil
		}

		req, err := http.NewRequestWithContext(downloadContext{resp.Request.Context()}, http.MethodGet, u.String(), nil)
		if err != nil {
			return errors.New("proxy: invalid download redirect")
		}

		_ = resp.Body.Close()
		next, err := h.transport.RoundTrip(req) //nolint:bodyclose // resp takes over next's body; ReverseProxy closes it.
		if err != nil {
			return errors.New("proxy: download unavailable")
		}

		*resp = *next
		resp.Request = req
	}

	if isDownloadRedirect(resp.StatusCode) {
		return errors.New("proxy: too many download redirects")
	}

	cleanDownloadResponse(resp)

	return nil
}

// Preserve cancellation and deadlines without forwarding the reverse proxy's
// 1xx response trace or connection identity to an external storage request.
type downloadContext struct{ context.Context }

func (downloadContext) Value(any) any { return nil }

// Storage response metadata must not set proxy cookies or expose signed URLs.
func cleanDownloadResponse(resp *http.Response) {
	header := http.Header{}
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Encoding"} {
		for _, value := range resp.Header.Values(name) {
			header.Add(name, value)
		}
	}

	resp.Header = header
	resp.Trailer = nil
}

func isDownloadRedirect(code int) bool {
	return code == 301 || code == 302 || code == 303 || code == 307 || code == 308
}
