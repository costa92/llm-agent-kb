// Package fetch performs SSRF-safe outbound HTTP for URL document ingest
// (spec §16.3). It enforces a scheme allowlist (http/https), resolves DNS and
// rejects any non-public resolved IP, dials the resolved IP DIRECTLY to defeat
// DNS rebinding, re-validates every redirect hop, applies connect/read
// timeouts and a max body cap, and enforces a response Content-Type allowlist.
package fetch

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config configures a Fetcher.
type Config struct {
	Timeout             time.Duration
	MaxBytes            int64
	AllowedContentTypes []string // matched as a prefix against the response media type

	// resolve overrides DNS resolution (tests inject a stub). nil → net.DefaultResolver.
	resolve func(ctx context.Context, host string) ([]net.IP, error)
	// allowLoopback permits loopback IPs (test-only; httptest servers are loopback).
	allowLoopback bool
}

// Fetcher fetches remote documents safely.
type Fetcher struct {
	cfg    Config
	client *http.Client
}

// New builds a Fetcher whose transport dials only validated, resolved IPs.
func New(cfg Config) *Fetcher {
	if cfg.resolve == nil {
		cfg.resolve = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	f := &Fetcher{cfg: cfg}
	dialer := &net.Dialer{Timeout: cfg.Timeout}
	transport := &http.Transport{
		// DialContext receives the host:port the http client wants to reach.
		// We resolve the host ourselves, validate every candidate IP, and dial
		// the validated IP directly — so the IP we checked is the IP we connect
		// to (no TOCTOU / DNS-rebinding window).
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ip, err := f.resolveAndValidate(ctx, host)
			if err != nil {
				return nil, err
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		},
		TLSHandshakeTimeout:   cfg.Timeout,
		ResponseHeaderTimeout: cfg.Timeout,
		DisableKeepAlives:     true,
	}
	f.client = &http.Client{
		Timeout:   cfg.Timeout,
		Transport: transport,
		// Re-validate every redirect hop's scheme + host BEFORE following it.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("fetch: too many redirects")
			}
			if err := validateScheme(req.URL); err != nil {
				return err
			}
			// Resolution+IP validation happens again in DialContext on the new
			// connection; this rejects an obviously-bad scheme early.
			return nil
		},
	}
	return f
}

// Get fetches the URL and returns the (capped) body + response media type.
func (f *Fetcher) Get(ctx context.Context, rawURL string) ([]byte, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("fetch: parse url: %w", err)
	}
	if err := validateScheme(u); err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "llm-agent-kb/url-ingest")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch: get %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("fetch: status %d for %s", resp.StatusCode, rawURL)
	}
	ct := resp.Header.Get("Content-Type")
	if !f.contentTypeAllowed(ct) {
		return nil, "", fmt.Errorf("fetch: content-type %q not allowed", ct)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.cfg.MaxBytes))
	if err != nil {
		return nil, "", fmt.Errorf("fetch: read body: %w", err)
	}
	return body, ct, nil
}

func (f *Fetcher) contentTypeAllowed(ct string) bool {
	media := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])
	for _, a := range f.cfg.AllowedContentTypes {
		if strings.HasPrefix(media, a) {
			return true
		}
	}
	return false
}

// resolveAndValidate resolves host to IPs and returns the first that passes the
// SSRF block check; if every candidate is blocked it errors.
func (f *Fetcher) resolveAndValidate(ctx context.Context, host string) (net.IP, error) {
	// A literal IP host is validated directly (no DNS).
	if literal := net.ParseIP(host); literal != nil {
		if f.blocked(literal) {
			return nil, fmt.Errorf("fetch: blocked IP %s", literal)
		}
		return literal, nil
	}
	ips, err := f.cfg.resolve(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("fetch: resolve %s: %w", host, err)
	}
	for _, ip := range ips {
		if !f.blocked(ip) {
			return ip, nil
		}
	}
	return nil, fmt.Errorf("fetch: all resolved IPs for %s are blocked", host)
}

func (f *Fetcher) blocked(ip net.IP) bool {
	if f.cfg.allowLoopback && ip.IsLoopback() {
		return false
	}
	return isBlockedIP(ip)
}

func validateScheme(u *url.URL) error {
	switch u.Scheme {
	case "http", "https":
		return nil
	default:
		return fmt.Errorf("fetch: scheme %q not allowed (http/https only)", u.Scheme)
	}
}

// cgnat is the RFC 6598 carrier-grade NAT range (100.64.0.0/10).
var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// isBlockedIP returns true for any IP that is NOT a routable public address:
// loopback, private, link-local (incl. 169.254.169.254 metadata), multicast,
// unspecified, interface-local, and RFC 6598 CGNAT.
func isBlockedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if v4 := ip.To4(); v4 != nil && cgnat.Contains(v4) {
		return true
	}
	return false
}
