package audit

import (
	"errors"
	"net/netip"
	"strings"
)

type Origin struct {
	IP     string
	Source string
}

func ParseTrustedProxies(values []string) ([]netip.Prefix, error) {
	if len(values) > 32 {
		return nil, errors.New("invalid audit trusted proxy policy")
	}
	result := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Bits() == 0 {
			return nil, errors.New("invalid audit trusted proxy policy")
		}
		if prefix.Addr().Is4In6() {
			if prefix.Bits() < 96 {
				return nil, errors.New("invalid audit trusted proxy policy")
			}
			prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
			if prefix.Bits() == 0 {
				return nil, errors.New("invalid audit trusted proxy policy")
			}
		}
		result = append(result, prefix.Masked())
	}
	return result, nil
}

// ResolveOrigin uses the actual socket peer by default. Forwarded values are
// considered only behind an explicitly trusted peer, from right to left. The
// first untrusted hop ends traversal, so a client's leftmost claim is ignored.
func ResolveOrigin(peer netip.Addr, forwarded string, trusted []netip.Prefix) Origin {
	valid := func(addr netip.Addr) bool {
		return addr.IsValid() && !addr.IsUnspecified() && !addr.IsMulticast() && addr.Zone() == ""
	}
	peer = peer.Unmap()
	if !valid(peer) {
		return Origin{Source: "unknown"}
	}
	fallback := Origin{IP: peer.String(), Source: "peer"}
	isTrusted := func(addr netip.Addr) bool {
		for _, prefix := range trusted {
			if prefix.Contains(addr) {
				return true
			}
		}
		return false
	}
	if !isTrusted(peer) || forwarded == "" || len(forwarded) > 1024 {
		return fallback
	}
	parts := strings.Split(forwarded, ",")
	if len(parts) > 20 {
		return fallback
	}
	chain := make([]netip.Addr, len(parts))
	for i, part := range parts {
		addr, err := netip.ParseAddr(strings.TrimSpace(part))
		if err != nil || !valid(addr) {
			return fallback
		}
		chain[i] = addr.Unmap()
	}
	chosen := peer
	for i := len(chain) - 1; i >= 0 && isTrusted(chosen); i-- {
		chosen = chain[i]
	}
	return Origin{IP: chosen.String(), Source: "forwarded"}
}
