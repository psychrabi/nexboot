package assets

import (
	"context"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
	"github.com/tianwei/diskless/internal/store"
)

// ClientNetwork describes the server's client-facing network: the dnsmasq interface, IPv4 subnet and locally assigned addresses,
// plus whether cross-subnet groups are supported (enable only when the switch provides DHCP relay).
// Known is false if the interface or its IP is unavailable; classification must not rely on unknown data.
type ClientNetwork struct {
	Iface            string
	Prefixes         []netip.Prefix
	ServerAddrs      []netip.Addr
	RouteGateway     netip.Addr // Server default gateway, if available
	AllowCrossSubnet bool
	Known            bool
}

// Contains tests whether an IP belongs to the client subnet.
func (n ClientNetwork) Contains(addr netip.Addr) bool {
	for _, prefix := range n.Prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// IsServerAddr tests whether an address belongs to the server.
func (n ClientNetwork) IsServerAddr(addr netip.Addr) bool {
	for _, a := range n.ServerAddrs {
		if a == addr {
			return true
		}
	}
	return false
}

// ServerAddrsInGroup lists server/VIP addresses overlapping the group range, including newly joined nodes.
func ServerAddrsInGroup(group domain.Group, net ClientNetwork) []netip.Addr {
	pool, err := groupAddressPool(group.StartIP, group.ClientMax)
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range net.ServerAddrs {
		if a.Is4() && pool.contains(ipv4Uint32(a)) {
			out = append(out, a)
		}
	}
	return out
}

// AddressUsers lists groups covering an IP and clients assigned that IP. Failures yield no users.
func AddressUsers(ctx context.Context, st store.Store, ip string) []string {
	addr, err := parseRequiredIPv4(ip)
	if err != nil {
		return nil
	}
	var users []string
	if groups, err := st.Groups().List(ctx); err == nil {
		for _, g := range groups {
			if pool, err := groupAddressPool(g.StartIP, g.ClientMax); err == nil && pool.contains(ipv4Uint32(addr)) {
				users = append(users, "address range of group " + g.Name)
			}
		}
	}
	if terminals, err := st.Terminals().List(ctx); err == nil {
		for _, t := range terminals {
			if strings.TrimSpace(t.IP) == addr.String() {
				name := t.Name
				if name == "" {
					name = t.MAC
				}
				users = append(users, "client " + name)
			}
		}
	}
	return users
}

// NetworkProvider supplies client network information to GroupService.
type NetworkProvider func(context.Context) (ClientNetwork, error)

// NetworkStatus classifies a group range relative to the server's client network.
type NetworkStatus string

const (
	NetworkSame    NetworkStatus = "same"    // Same subnet: DHCP broadcasts reach the server
	NetworkRelay   NetworkStatus = "relay"   // Other subnet with DHCP relay: allowed; gateway is relay interface
	NetworkBlocked NetworkStatus = "blocked" // Other subnet without relay: clients cannot get an address
	NetworkUnknown NetworkStatus = "unknown" // Server network is unavailable
)

// ClassifyGroupNetwork is the shared validation used by forms, create/update, alarms and network settings.
func ClassifyGroupNetwork(group domain.Group, net ClientNetwork) NetworkStatus {
	if !net.Known || len(net.Prefixes) == 0 {
		return NetworkUnknown
	}
	pool, err := groupAddressPool(group.StartIP, group.ClientMax)
	if err != nil {
		return NetworkUnknown
	}
	start, end := addrOf(pool.start), addrOf(pool.end)
	for _, prefix := range net.Prefixes {
		if prefix.Contains(start) && prefix.Contains(end) {
			return NetworkSame
		}
	}
	if net.AllowCrossSubnet {
		return NetworkRelay
	}
	return NetworkBlocked
}

// SuggestedRange is the generated client IP range used for initial form defaults and invalid-range alternatives.
type SuggestedRange struct {
	StartIP   string `json:"start_ip"`
	ClientMax int    `json:"client_max"`
	Netmask   string `json:"netmask"`
	Gateway   string `json:"gateway"`
}

// SuggestGroupRange selects count consecutive addresses, searching from .10 then .2 in the client subnet,
// excluding server, reserved and other group addresses (except the group currently being edited).
// Suggest the server default gateway only if it belongs to the client subnet.
func SuggestGroupRange(net ClientNetwork, others []domain.Group, count int, exceptGroupID string) (SuggestedRange, bool) {
	if count <= 0 || len(net.Prefixes) == 0 {
		return SuggestedRange{}, false
	}
	taken := make([]addressPool, 0, len(others))
	for _, g := range others {
		if g.ID == exceptGroupID {
			continue
		}
		if pool, err := groupAddressPool(g.StartIP, g.ClientMax); err == nil {
			taken = append(taken, pool)
		}
	}
	for _, prefix := range net.Prefixes {
		prefix = prefix.Masked()
		if !prefix.Addr().Is4() {
			continue
		}
		network := ipv4Uint32(prefix.Addr())
		mask := uint32(0xffffffff) << (32 - prefix.Bits())
		broadcast := network | ^mask
		blocked := func(n uint32) bool {
			if n == network || n == broadcast || net.IsServerAddr(addrOf(n)) {
				return true
			}
			for _, p := range taken {
				if p.contains(n) {
					return true
				}
			}
			return false
		}
		for _, first := range []uint32{network + 10, network + 2} {
			for start := first; start+uint32(count)-1 <= broadcast && start >= first; start++ {
				fits := true
				for n := start; n < start+uint32(count); n++ {
					if blocked(n) {
						fits = false
						start = n // Skip occupied range
						break
					}
				}
				if fits {
					gateway := ""
					if net.RouteGateway.IsValid() && prefix.Contains(net.RouteGateway) {
						gateway = net.RouteGateway.String()
					}
					return SuggestedRange{StartIP: ipv4String(start), ClientMax: count, Netmask: netmaskString(prefix.Bits()), Gateway: gateway}, true
				}
			}
		}
	}
	return SuggestedRange{}, false
}

func addrOf(n uint32) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}

func netmaskString(bits int) string {
	mask := uint32(0xffffffff) << (32 - bits)
	return ipv4String(mask)
}

// GroupOutsideClientNetwork is reported for ranges outside the client subnet with no DHCP relay.
// Include the NIC, subnet, rejected range and a usable alternative or required feature toggle.
type GroupOutsideClientNetwork struct {
	Group   domain.Group
	Network ClientNetwork
	Suggest *SuggestedRange
}

func (e GroupOutsideClientNetwork) Error() string {
	pool, _ := groupAddressPool(e.Group.StartIP, e.Group.ClientMax)
	nets := make([]string, 0, len(e.Network.Prefixes))
	for _, p := range e.Network.Prefixes {
		nets = append(nets, p.String())
	}
	sort.Strings(nets)
	msg := fmt.Sprintf("Group %s range %s–%s is outside server client interface %s (%s); clients will not receive addresses",
		e.Group.Name, ipv4String(pool.start), ipv4String(pool.end), e.Network.Iface, strings.Join(nets, "、"))
	if e.Suggest != nil {
		end, _ := groupAddressPool(e.Suggest.StartIP, e.Suggest.ClientMax)
		msg += fmt.Sprintf("; use %s–%s instead", e.Suggest.StartIP, ipv4String(end.end))
	} else {
		msg += "; move the group to the server subnet"
	}
	return msg + ". If clients are on another VLAN, enable Cross-subnet Groups in System Settings"
}

func (e GroupOutsideClientNetwork) Is(target error) bool { return target == errs.ErrInvalid }

// clientNetwork reads provider data; missing providers (tests or unsupported nodes) disable classification.
func (s GroupService) clientNetwork(ctx context.Context) ClientNetwork {
	if s.Network == nil {
		return ClientNetwork{}
	}
	net, err := s.Network(ctx)
	if err != nil {
		warnDHCP(s.Logger, "could not read the client network; skipping the network check", err)
		return ClientNetwork{}
	}
	return net
}

// ensureGroupOnClientNetwork rejects unreachable ranges. Gateway is not checked here because the server may route between NICs.
func (s GroupService) ensureGroupOnClientNetwork(ctx context.Context, group domain.Group, others []domain.Group) error {
	net := s.clientNetwork(ctx)
	if ClassifyGroupNetwork(group, net) != NetworkBlocked {
		return nil
	}
	err := GroupOutsideClientNetwork{Group: group, Network: net}
	if suggest, ok := SuggestGroupRange(net, others, group.ClientMax, group.ID); ok {
		err.Suggest = &suggest
	}
	return err
}
