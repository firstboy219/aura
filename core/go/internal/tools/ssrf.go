// Package tools implements the Phase 1 tools (E.2 item 7). The SSRF checker
// here is shared with the P4 browser egress proxy (ADR-0010).
package tools

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

var ErrBlockedAddress = errors.New("ssrf_blocked_address")
var ErrBlockedURL = errors.New("ssrf_blocked_url")

var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
		"::/128", "::1/128", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "fc00::/7", "fe80::/10",
		"fec0::/10", "ff00::/8",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// Blocked reports whether an address must never be dialled: private,
// loopback, link-local (incl. 169.254.169.254 metadata), CGNAT, multicast,
// reserved, documentation, and IPv4 embedded in IPv4-mapped or NAT64 IPv6.
func Blocked(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.IsUnspecified() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() {
		return true
	}
	if a.Is6() && nat64.Contains(a) {
		b := a.As16()
		return Blocked(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// Resolver is the DNS dependency (replaceable in tests).
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Guard validates URLs and produces a DialContext that only connects to
// addresses it has itself resolved and checked, so a DNS answer cannot change
// between the check and the connection (rebinding).
type Guard struct {
	Resolver Resolver
	// allowLoopback is for in-process fixture servers in tests only; it is not
	// reachable from configuration.
	allowLoopback bool
	// portOverride redirects every dial to a fixture server port. Tests only.
	portOverride string
}

// NewGuard returns a guard using the system resolver.
func NewGuard() *Guard { return &Guard{Resolver: net.DefaultResolver} }

// NewTestGuard allows loopback addresses (fixture servers). Tests only.
func NewTestGuard(r Resolver) *Guard {
	if r == nil {
		r = net.DefaultResolver
	}
	return &Guard{Resolver: r, allowLoopback: true}
}

func (g *Guard) blocked(a netip.Addr) bool {
	if g.allowLoopback && a.Unmap().IsLoopback() {
		return false
	}
	return Blocked(a)
}

// CheckURL validates scheme, port, userinfo and the domain allowlist.
func CheckURL(u *url.URL, allow []string) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q", ErrBlockedURL, u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("%w: userinfo not allowed", ErrBlockedURL)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return fmt.Errorf("%w: empty host", ErrBlockedURL)
	}
	if p := u.Port(); p != "" && !(u.Scheme == "http" && p == "80") && !(u.Scheme == "https" && p == "443") {
		return fmt.Errorf("%w: port %s", ErrBlockedURL, p)
	}
	if !DomainAllowed(host, allow) {
		return fmt.Errorf("%w: domain %s not in allowlist", ErrBlockedURL, host)
	}
	return nil
}

// DomainAllowed matches exact hosts, or subdomains for entries "*.example.com".
func DomainAllowed(host string, allow []string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for _, d := range allow {
		d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
		if d == host {
			return true
		}
		if strings.HasPrefix(d, "*.") && strings.HasSuffix(host, d[1:]) {
			return true
		}
	}
	return false
}

// DialContext resolves, rejects the whole host if any address is blocked, and
// dials the checked IP literal.
func (g *Guard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var addrs []netip.Addr
	if ip, perr := netip.ParseAddr(host); perr == nil {
		addrs = []netip.Addr{ip}
	} else {
		addrs, err = g.Resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%w: %s has no addresses", ErrBlockedAddress, host)
	}
	for _, a := range addrs {
		if g.blocked(a) {
			return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlockedAddress, host, a)
		}
	}
	if g.portOverride != "" {
		port = g.portOverride
	}
	var d net.Dialer
	var lastErr error
	for _, a := range addrs {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// SetTestPort makes a test guard dial every connection to a fixture port.
// It refuses guards that were not created with NewTestGuard.
func SetTestPort(g *Guard, port string) {
	if !g.allowLoopback {
		panic("tools: SetTestPort on a production guard")
	}
	g.portOverride = port
}
