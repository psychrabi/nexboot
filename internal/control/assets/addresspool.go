package assets

// addressPool represents a group's contiguous ClientMax IPv4 addresses starting at StartIP.
// Centralize group IP range membership, allocation and boundary calculations here.
type addressPool struct {
	start, end uint32
}

// groupAddressPool builds a range from StartIP and ClientMax, rejecting non-IPv4, nonpositive sizes and IPv4 overflow.
func groupAddressPool(startIP string, clientMax int) (addressPool, error) {
	start, err := parseRequiredIPv4(startIP)
	if err != nil {
		return addressPool{}, err
	}
	if clientMax <= 0 {
		return addressPool{}, ErrGroupInvalidNetwork
	}
	startN := ipv4Uint32(start)
	offset := uint64(clientMax - 1)
	if offset > uint64(^uint32(0)-startN) {
		return addressPool{}, ErrGroupInvalidNetwork
	}
	return addressPool{start: startN, end: startN + uint32(offset)}, nil
}

func (p addressPool) contains(n uint32) bool {
	return n >= p.start && n <= p.end
}

// overlaps detects whether two groups could assign the same addresses; each group creates its own dhcp-range,
// so overlaps can cause dnsmasq to assign an unintended gateway.
func (p addressPool) overlaps(other addressPool) bool {
	return p.start <= other.end && other.start <= p.end
}

// coversReserved checks whether the range includes the network or broadcast address, which clients cannot use.
// dnsmasq still accepts them. Calculate with the subnet mask: under /23, x.10.255 can be a valid host.
// validateGroupNetwork must first ensure start and end are in the same subnet.
func (p addressPool) coversReserved(mask uint32) bool {
	network := p.start & mask
	return p.contains(network) || p.contains(network|^mask)
}

// allocate returns the lowest unallocated address in the range.
func (p addressPool) allocate(occupied map[uint32]bool) (string, bool) {
	for current := p.start; ; current++ {
		if !occupied[current] {
			return ipv4String(current), true
		}
		if current == p.end {
			return "", false
		}
	}
}
