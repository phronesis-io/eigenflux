package publicurl

import (
	"net"
	"strconv"
	"strings"

	"eigenflux_server/pkg/skilldoc"
)

func Resolve(configuredBaseURL string, port int) string {
	if normalized := skilldoc.NormalizePublicBaseURL(configuredBaseURL); normalized != "" {
		return normalized
	}
	return "http://" + net.JoinHostPort(bestShareHost(), strconv.Itoa(port))
}

func bestShareHost() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "127.0.0.1"
	}

	var fallback string
	for _, iface := range ifaces {
		if !isCandidateShareInterface(iface) {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			ip := ipv4FromAddr(addr)
			if ip == nil || !isCandidateShareIP(ip) {
				continue
			}
			if isRFC1918PrivateIPv4(ip) {
				return ip.String()
			}
			if fallback == "" {
				fallback = ip.String()
			}
		}
	}

	if fallback != "" {
		return fallback
	}
	return "127.0.0.1"
}

func isCandidateShareInterface(iface net.Interface) bool {
	if iface.Flags&net.FlagUp == 0 {
		return false
	}
	if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagPointToPoint != 0 {
		return false
	}

	name := strings.ToLower(strings.TrimSpace(iface.Name))
	virtualPrefixes := []string{
		"lo", "utun", "awdl", "llw", "gif", "stf", "bridge", "anpi", "ap",
		"docker", "veth", "br-", "tun", "tap", "tailscale", "wg", "vmnet",
	}
	for _, prefix := range virtualPrefixes {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}

	return true
}

func ipv4FromAddr(addr net.Addr) net.IP {
	ipNet, ok := addr.(*net.IPNet)
	if !ok || ipNet.IP == nil {
		return nil
	}
	return ipNet.IP.To4()
}

func isCandidateShareIP(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || !ip.IsGlobalUnicast() {
		return false
	}

	// 169.254.0.0/16 is link-local and not suitable for sharing.
	if ip[0] == 169 && ip[1] == 254 {
		return false
	}
	// 198.18.0.0/15 is reserved for benchmark/testing networks and is often used by TUN adapters.
	if ip[0] == 198 && (ip[1] == 18 || ip[1] == 19) {
		return false
	}
	// 100.64.0.0/10 is carrier-grade NAT space and is usually not directly shareable on local networks.
	if ip[0] == 100 && ip[1] >= 64 && ip[1] <= 127 {
		return false
	}

	return true
}

func isRFC1918PrivateIPv4(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip[0] == 10 {
		return true
	}
	if ip[0] == 172 && ip[1] >= 16 && ip[1] <= 31 {
		return true
	}
	if ip[0] == 192 && ip[1] == 168 {
		return true
	}
	return false
}
