package source

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

type trustedDialer struct {
	host         string
	resolver     IPResolver
	allowPrivate bool
	base         net.Dialer
}

func (dialer *trustedDialer) DialContext(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid dial address", ErrUntrustedEndpoint)
	}
	if !sameHost(host, dialer.host) {
		return nil, fmt.Errorf("%w: transport attempted a different origin",
			ErrUntrustedEndpoint)
	}
	addresses, err := resolveTrustedIPs(
		ctx,
		dialer.resolver,
		dialer.host,
		dialer.allowPrivate,
	)
	if err != nil {
		return nil, err
	}
	var dialErrors []error
	for _, resolved := range addresses {
		connection, dialErr := dialer.base.DialContext(
			ctx,
			networkForIP(network, resolved),
			net.JoinHostPort(resolved.String(), port),
		)
		if dialErr == nil {
			return connection, nil
		}
		dialErrors = append(dialErrors, dialErr)
	}
	return nil, fmt.Errorf("dial trusted delivery source: %w", errors.Join(dialErrors...))
}

func resolveTrustedIPs(
	ctx context.Context,
	resolver IPResolver,
	host string,
	allowPrivate bool,
) ([]netip.Addr, error) {
	var (
		addresses []netip.Addr
		err       error
	)
	if literal, parseErr := netip.ParseAddr(host); parseErr == nil {
		addresses = []netip.Addr{literal}
	} else {
		addresses, err = resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve delivery source origin: %w", err)
		}
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("%w: origin resolved to no addresses", ErrUntrustedEndpoint)
	}
	result := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsValid() || (!allowPrivate && unsafeSourceIP(address)) {
			return nil, fmt.Errorf("%w: origin resolved to a prohibited address",
				ErrUntrustedEndpoint)
		}
		result = append(result, address)
	}
	return result, nil
}

func unsafeSourceIP(address netip.Addr) bool {
	return address.IsUnspecified() ||
		address.IsLoopback() ||
		address.IsPrivate() ||
		address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() ||
		address.IsInterfaceLocalMulticast() ||
		address.IsMulticast()
}

func sameHost(left, right string) bool {
	return strings.EqualFold(
		strings.TrimSuffix(left, "."),
		strings.TrimSuffix(right, "."),
	)
}

func networkForIP(network string, address netip.Addr) string {
	switch network {
	case "tcp", "tcp4", "tcp6":
		if address.Is4() {
			return "tcp4"
		}
		return "tcp6"
	default:
		return network
	}
}
