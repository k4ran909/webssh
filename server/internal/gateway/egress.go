package gateway

import (
	"context"
	"net"
	"net/netip"
	"time"
)

// Ranges that are not covered by netip's Is* helpers but must never be
// reachable from a public gateway.
var restrictedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can reach private IPv4
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

func isRestricted(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return true
	}
	for _, p := range restrictedPrefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// resolveTarget resolves host once and returns the address to dial. Dialing
// the checked IP (not the name) closes the DNS-rebinding hole between check
// and connect. IPv4 is preferred because broken IPv6 routes cause slow
// timeouts on many home networks.
func resolveTarget(ctx context.Context, host string, allowPrivate bool) (netip.Addr, error) {
	var ips []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{ip}
	} else {
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		ips, err = net.DefaultResolver.LookupNetIP(lctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return netip.Addr{}, &gwError{codeDNS, "Could not resolve " + host}
		}
	}

	for _, want4 := range []bool{true, false} {
		for _, ip := range ips {
			ip = ip.Unmap()
			if ip.Is4() == want4 && (allowPrivate || !isRestricted(ip)) {
				return ip, nil
			}
		}
	}
	return netip.Addr{}, &gwError{codeBlockedDest, "This gateway does not allow connections to private or internal addresses."}
}
