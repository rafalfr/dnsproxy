package proxy

import (
	"net"

	"github.com/AdguardTeam/golibs/netutil"
	"github.com/miekg/dns"
)

// rafal code
// //////////////////////////////////////////////////////////////////////////////
func GenEmptyMessage(request *dns.Msg, rCode int, retry uint32) *dns.Msg {
	resp := dns.Msg{}
	resp.SetRcode(request, rCode)
	resp.RecursionAvailable = true
	resp.Ns = genSOA(request, retry)
	return &resp
}

// genSOA returns SOA for an authority section
func genSOA(request *dns.Msg, retry uint32) []dns.RR {
	zone := ""
	if len(request.Question) > 0 {
		zone = request.Question[0].Name
	}

	soa := dns.SOA{
		// values copied from verisign's nonexistent .com domain
		// their exact values are not important in our use case because they are used for domain transfers between primary/secondary DNS servers
		Refresh: 1800,
		Retry:   retry,
		Expire:  604800,
		Minttl:  86400,
		// copied from AdGuard DNS
		Ns:     "fake-for-negative-caching.adguard.com.",
		Serial: 100500,
		// rest is request-specific
		Hdr: dns.RR_Header{
			Name:   zone,
			Rrtype: dns.TypeSOA,
			Ttl:    3600,
			Class:  dns.ClassINET,
		},
	}
	soa.Mbox = "hostmaster."
	if len(zone) > 0 && zone[0] != '.' {
		soa.Mbox += zone
	}
	return []dns.RR{&soa}
}

////////////////////////////////////////////////////////////////////////////////
// end rafal code

// ecsFromMsg returns the subnet from EDNS Client Subnet option of m if any.
func ecsFromMsg(m *dns.Msg) (subnet *net.IPNet, scope int) {
	opt := m.IsEdns0()
	if opt == nil {
		return nil, 0
	}

	var ip net.IP
	var mask net.IPMask
	for _, e := range opt.Option {
		sn, ok := e.(*dns.EDNS0_SUBNET)
		if !ok {
			continue
		}

		switch sn.Family {
		case 1:
			ip = sn.Address.To4()
			mask = net.CIDRMask(int(sn.SourceNetmask), netutil.IPv4BitLen)
		case 2:
			ip = sn.Address
			mask = net.CIDRMask(int(sn.SourceNetmask), netutil.IPv6BitLen)
		default:
			continue
		}

		return &net.IPNet{IP: ip, Mask: mask}, int(sn.SourceScope)
	}

	return nil, 0
}

// setECS sets the EDNS client subnet option based on ip and scope into m.  It
// returns masked IP and mask length.
func setECS(m *dns.Msg, ip net.IP, scope uint8) (subnet *net.IPNet) {
	const (
		// defaultECSv4 is the default length of network mask for IPv4 address
		// in ECS option.
		defaultECSv4 = 24

		// defaultECSv6 is the default length of network mask for IPv6 address
		// in ECS.  The size of 7 octets is chosen as a reasonable minimum since
		// at least Google's public DNS refuses requests containing the options
		// with longer network masks.
		defaultECSv6 = 56
	)

	e := &dns.EDNS0_SUBNET{
		Code:        dns.EDNS0SUBNET,
		SourceScope: scope,
	}

	subnet = &net.IPNet{}
	if ip4 := ip.To4(); ip4 != nil {
		e.Family = 1
		e.SourceNetmask = defaultECSv4
		subnet.Mask = net.CIDRMask(defaultECSv4, netutil.IPv4BitLen)
		ip = ip4
	} else {
		// Assume the IP address has already been validated.
		e.Family = 2
		e.SourceNetmask = defaultECSv6
		subnet.Mask = net.CIDRMask(defaultECSv6, netutil.IPv6BitLen)
	}
	subnet.IP = ip.Mask(subnet.Mask)
	e.Address = subnet.IP

	// If OPT record already exists so just add EDNS option inside it.  Note
	// that servers may return FORMERR if they meet several OPT RRs.
	if opt := m.IsEdns0(); opt != nil {
		opt.Option = append(opt.Option, e)

		return subnet
	}

	// Create an OPT record and add EDNS option inside it.
	o := &dns.OPT{
		Hdr: dns.RR_Header{
			Name:   ".",
			Rrtype: dns.TypeOPT,
		},
		Option: []dns.EDNS0{e},
	}
	o.SetUDPSize(4096)
	m.Extra = append(m.Extra, o)

	return subnet
}
