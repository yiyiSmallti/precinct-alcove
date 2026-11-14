//go:build e2e

// What left the machine during a test, read from a packet capture taken on
// the one link out of it (e2e/personal.sh puts the test in a network
// namespace whose only way out is a veth pair and captures on the far end).
//
// Ways this can give a wrong answer, and what guards against each:
//   - nothing was captured at all (wrong interface, tcpdump died): the model
//     endpoint is reached over the captured link, so a capture without model
//     traffic fails the step instead of passing it;
//   - the capture stopped part-way: the test opens one last connection to
//     the model endpoint and waits to see it in the file (captureMark);
//   - the other end's own packets counted as ours (the relay calling the
//     model, its router solicitations): only frames sent from the namespace's
//     interface, by MAC address, are counted;
//   - a TCP answer counted as an outgoing connection: only a SYN without ACK
//     opens a flow;
//   - IPv6 not looked at: both families are read, extension headers skipped;
//   - a name looked up but never called: DNS questions are kept with the flow.
package e2e

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"sort"
	"strings"
)

// flow is one kind of traffic sent from the machine under test.
type flow struct {
	Proto string     // "tcp", "udp", "icmp", "icmpv6" or "ip-<protocol number>"
	Dst   netip.Addr // where it was sent
	Port  uint16     // destination port; 0 where the protocol has none
	// Count is connections opened for TCP, packets for everything else.
	Count int
	// Names are the DNS names asked for, when this is a query (udp/53).
	Names []string
}

func (f flow) String() string {
	s := fmt.Sprintf("%s %s", f.Proto, f.Dst)
	if f.Port != 0 {
		s = fmt.Sprintf("%s %s", f.Proto, netip.AddrPortFrom(f.Dst, f.Port))
	}
	s += fmt.Sprintf(" ×%d", f.Count)
	if len(f.Names) > 0 {
		s += "（查询 " + strings.Join(f.Names, "、") + "）"
	}
	return s
}

// allowedFlow reports whether the personal edition may send f. The rule it
// stands for is the delivery checklist's "安装与使用全程无任何对外流量（模型 API
// 除外）"; model is the model endpoint the user configured.
func allowedFlow(f flow, model netip.AddrPort) bool {
	// The one exception the rule names: connections to the model endpoint,
	// address and port both. Another port on the same host is another
	// service.
	if f.Proto == "tcp" && f.Dst == model.Addr() && f.Port == model.Port() {
		return true
	}
	// What the kernel says on the link when an interface comes up (router
	// solicitations, listener reports, duplicate address detection): never
	// routed past the link, and nothing the application sent.
	//
	// Everything else is refused, a DNS question included: a name that was
	// looked up is a call that was about to be made, whether or not it went
	// through.
	return f.Proto == "icmpv6" && f.Dst.IsLinkLocalMulticast()
}

// readCapture lists what the interface with address mac sent, from a pcap
// file. A last record still being written is ignored.
func readCapture(path string, mac net.HardwareAddr) ([]flow, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 24 {
		return nil, errors.New("capture: no pcap header yet")
	}
	var order binary.ByteOrder
	switch binary.LittleEndian.Uint32(b) {
	case 0xa1b2c3d4, 0xa1b23c4d:
		order = binary.LittleEndian
	case 0xd4c3b2a1, 0x4d3cb2a1:
		order = binary.BigEndian
	default:
		return nil, fmt.Errorf("capture: not a pcap file (magic %x)", b[:4])
	}
	if lt := order.Uint32(b[20:]); lt != 1 {
		return nil, fmt.Errorf("capture: link type %d, want Ethernet (1)", lt)
	}
	type key struct {
		proto string
		dst   netip.Addr
		port  uint16
	}
	flows := map[key]*flow{}
	add := func(proto string, dst netip.Addr, port uint16, name string) {
		k := key{proto, dst, port}
		f := flows[k]
		if f == nil {
			f = &flow{Proto: proto, Dst: dst, Port: port}
			flows[k] = f
		}
		f.Count++
		if name != "" && !contains(f.Names, name) {
			f.Names = append(f.Names, name)
		}
	}
	for b = b[24:]; len(b) >= 16; {
		n := int(order.Uint32(b[8:]))
		if n < 0 || len(b) < 16+n {
			break
		}
		frame := b[16 : 16+n]
		b = b[16+n:]
		if len(frame) < 14 || !equalMAC(frame[6:12], mac) {
			continue
		}
		var proto byte
		var dst netip.Addr
		var p []byte // the transport header onwards
		switch ip := frame[14:]; binary.BigEndian.Uint16(frame[12:]) {
		case 0x0800:
			if len(ip) < 20 || int(ip[0]&0x0f)*4 > len(ip) {
				continue
			}
			proto, dst = ip[9], netip.AddrFrom4([4]byte(ip[16:20]))
			// Only the first fragment carries the transport header.
			if binary.BigEndian.Uint16(ip[6:])&0x1fff == 0 {
				p = ip[int(ip[0]&0x0f)*4:]
			}
		case 0x86dd:
			if len(ip) < 40 {
				continue
			}
			proto, dst, p = ip[6], netip.AddrFrom16([16]byte(ip[24:40])), ip[40:]
			// Hop-by-hop, routing and destination options sit between the
			// fixed header and the transport header.
			for (proto == 0 || proto == 43 || proto == 60) && len(p) >= 8 && len(p) >= (int(p[1])+1)*8 {
				proto, p = p[0], p[(int(p[1])+1)*8:]
			}
		default:
			// ARP and the like resolve addresses on the link; they carry
			// nothing off it.
			continue
		}
		switch proto {
		case 6:
			if len(p) < 14 {
				add("tcp", dst, 0, "")
			} else if p[13]&0x12 == 0x02 { // SYN without ACK: a connection we open
				add("tcp", dst, binary.BigEndian.Uint16(p[2:]), "")
			}
		case 17:
			if len(p) < 8 {
				add("udp", dst, 0, "")
				continue
			}
			port := binary.BigEndian.Uint16(p[2:])
			name := ""
			if port == 53 {
				name = dnsQuestion(p[8:])
			}
			add("udp", dst, port, name)
		case 1:
			add("icmp", dst, 0, "")
		case 58:
			add("icmpv6", dst, 0, "")
		default:
			add(fmt.Sprintf("ip-%d", proto), dst, 0, "")
		}
	}
	out := make([]flow, 0, len(flows))
	for _, f := range flows {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

// dnsQuestion is the first name a DNS query asks for, "" when it cannot be
// read.
func dnsQuestion(msg []byte) string {
	if len(msg) < 13 || binary.BigEndian.Uint16(msg[4:]) == 0 {
		return ""
	}
	var labels []string
	for p := msg[12:]; len(p) > 0; {
		n := int(p[0])
		if n == 0 {
			return strings.Join(labels, ".")
		}
		if n > 63 || len(p) < 1+n {
			return ""
		}
		labels = append(labels, string(p[1:1+n]))
		p = p[1+n:]
	}
	return ""
}

func equalMAC(a []byte, b net.HardwareAddr) bool { return string(a) == string(b) }

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// modelConnections is how many connections the capture shows to model.
func modelConnections(flows []flow, model netip.AddrPort) int {
	for _, f := range flows {
		if f.Proto == "tcp" && f.Dst == model.Addr() && f.Port == model.Port() {
			return f.Count
		}
	}
	return 0
}
