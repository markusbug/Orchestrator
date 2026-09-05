// Package netaddr enumerates addresses a phone could use to reach this host.
package netaddr

import (
	"net"
	"net/netip"
	"strings"

	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
)

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// List returns non-loopback unicast addresses of up interfaces, LAN first.
func List() []protocol.HostAddr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var lan, ts []protocol.HostAddr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		isTS := strings.HasPrefix(ifc.Name, "tailscale") || strings.HasPrefix(ifc.Name, "utun") && false
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap()
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || !ip.IsValid() {
				continue
			}
			if ip.Is6() && !ip.IsGlobalUnicast() {
				continue
			}
			if isTS || cgnat.Contains(ip) {
				ts = append(ts, protocol.HostAddr{IP: ip.String(), Kind: "tailscale"})
				continue
			}
			// Skip docker/bridge style interfaces that are rarely reachable.
			if strings.HasPrefix(ifc.Name, "docker") || strings.HasPrefix(ifc.Name, "br-") || strings.HasPrefix(ifc.Name, "veth") || strings.HasPrefix(ifc.Name, "virbr") {
				continue
			}
			lan = append(lan, protocol.HostAddr{IP: ip.String(), Kind: "lan"})
		}
	}
	return append(lan, ts...)
}
