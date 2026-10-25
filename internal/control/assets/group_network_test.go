package assets

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/tianwei/diskless/internal/control/errs"
	"github.com/tianwei/diskless/internal/domain"
)

func testClientNetwork(allowCross bool) ClientNetwork {
	return ClientNetwork{
		Iface:            "ndbr0",
		Prefixes:         []netip.Prefix{netip.MustParsePrefix("192.168.50.1/24")},
		ServerAddrs:      []netip.Addr{netip.MustParseAddr("192.168.50.1"), netip.MustParseAddr("192.168.124.56")},
		AllowCrossSubnet: allowCross,
		Known:            true,
	}
}

func groupWith(start string, max int, gateway string) domain.Group {
	return domain.Group{ID: "g", Name: "Class A", StartIP: start, ClientMax: max, Gateway: gateway, Netmask: "255.255.255.0"}
}

// Classify as same within the subnet, relay for allowed cross-subnet, blocked otherwise, and unknown if server network is unavailable.
func TestClassifyGroupNetwork(t *testing.T) {
	cases := []struct {
		name  string
		group domain.Group
		net   ClientNetwork
		want  NetworkStatus
	}{
		{"inside the client network", groupWith("192.168.50.50", 30, "192.168.50.254"), testClientNetwork(false), NetworkSame},
		{"outside, relay not allowed", groupWith("192.168.10.50", 30, "192.168.10.1"), testClientNetwork(false), NetworkBlocked},
		{"outside, relay allowed", groupWith("192.168.10.50", 30, "192.168.10.1"), testClientNetwork(true), NetworkRelay},
		{"window straddles the network edge", groupWith("192.168.50.240", 30, "192.168.50.254"), testClientNetwork(false), NetworkBlocked},
		{"network unreadable", groupWith("192.168.10.50", 30, "192.168.10.1"), ClientNetwork{}, NetworkUnknown},
	}
	for _, c := range cases {
		if got := ClassifyGroupNetwork(c.group, c.net); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// Suggestions start from .10 for new groups/autofill and avoid server addresses, reserved addresses and existing ranges.
// If .10 cannot fit the range, try from .2.
func TestSuggestGroupRange(t *testing.T) {
	net := testClientNetwork(false)
	got, ok := SuggestGroupRange(net, nil, 30, "")
	if !ok || got.StartIP != "192.168.50.10" || got.Netmask != "255.255.255.0" {
		t.Fatalf("empty network: %#v ok=%v", got, ok)
	}
	// .10–.39 is occupied by another group; the next free interval follows immediately.
	others := []domain.Group{{ID: "other", StartIP: "192.168.50.10", ClientMax: 30}}
	got, ok = SuggestGroupRange(net, others, 30, "")
	if !ok || got.StartIP != "192.168.50.40" {
		t.Fatalf("after another group: %#v ok=%v", got, ok)
	}
	// The group being edited does not block its own range.
	got, ok = SuggestGroupRange(net, others, 30, "other")
	if !ok || got.StartIP != "192.168.50.10" {
		t.Fatalf("editing the same group: %#v ok=%v", got, ok)
	}
	// Exclude server .1 and subnet .0/.255; if .10 is unavailable, retry from .2.
	big := []domain.Group{{ID: "a", StartIP: "192.168.50.10", ClientMax: 240}} // .10–.249
	got, ok = SuggestGroupRange(net, big, 8, "")
	if !ok || got.StartIP != "192.168.50.2" {
		t.Fatalf("fallback below .10: %#v ok=%v", got, ok)
	}
	if _, ok = SuggestGroupRange(net, big, 30, ""); ok {
		t.Fatal("30 addresses cannot fit; must report so")
	}
	// Recommend default gateway only if it lies within the client subnet.
	net.RouteGateway = netip.MustParseAddr("192.168.50.254")
	got, _ = SuggestGroupRange(net, nil, 30, "")
	if got.Gateway != "192.168.50.254" {
		t.Fatalf("gateway = %q, want the server's route gateway", got.Gateway)
	}
	net.RouteGateway = netip.MustParseAddr("192.168.124.1")
	got, _ = SuggestGroupRange(net, nil, 30, "")
	if got.Gateway != "" {
		t.Fatalf("gateway = %q, want none (route gateway is on another network)", got.Gateway)
	}
}

// Reject out-of-subnet groups early with NIC and alternatives; allow the same request when cross-subnet is enabled.
// Gateway may be blank when no router exists or may point to the server when it routes the classroom network.
func TestGroupServiceEnforcesClientNetwork(t *testing.T) {
	ctx := context.Background()
	st := newImageTestStore(t)
	seedGroupTriple(t, ctx, st, "img-1", "cfg-1", "red-1")
	service := newGroupService(st)
	service.DHCP = &fakeDHCPSyncer{}
	allow := false
	service.Network = func(context.Context) (ClientNetwork, error) { return testClientNetwork(allow), nil }

	req := validGroupRequest("Subnet Ten Classroom") // 192.168.1.10 + 20; gateway 192.168.1.1
	_, err := service.Create(ctx, req)
	var outside GroupOutsideClientNetwork
	if !errors.As(err, &outside) || !errors.Is(err, errs.ErrInvalid) {
		t.Fatalf("err = %v, want GroupOutsideClientNetwork", err)
	}
	for _, want := range []string{"ndbr0", "192.168.50.1/24", "192.168.1.10", "192.168.50.10", "Cross-subnet Groups"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err %q should mention %q", err.Error(), want)
		}
	}
	if outside.Suggest == nil || outside.Suggest.StartIP != "192.168.50.10" {
		t.Fatalf("suggest = %#v", outside.Suggest)
	}

	allow = true
	created, err := service.Create(ctx, req)
	if err != nil {
		t.Fatalf("with cross-subnet allowed the group must pass: %v", err)
	}
	if created.StartIP != "192.168.1.10" {
		t.Fatalf("created = %#v", created)
	}

	// Accept a blank gateway or the server's own IP when a second NIC routes the classroom subnet.
	req = validGroupRequest("Subnet Fifty Classroom")
	req.StartIP, req.Gateway = "192.168.50.50", ""
	if _, err := service.Create(ctx, req); err != nil {
		t.Fatalf("empty gateway must be allowed: %v", err)
	}
	req = validGroupRequest("Server Gateway")
	req.StartIP, req.Gateway = "192.168.50.100", "192.168.50.1"
	if _, err := service.Create(ctx, req); err != nil {
		t.Fatalf("gateway = server must be allowed: %v", err)
	}
}
