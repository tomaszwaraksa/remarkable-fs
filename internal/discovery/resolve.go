package discovery

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// mdnsGroup is the IPv4 multicast address and port that mDNS responders listen on.
var mdnsGroup = &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}

// IsMDNSName reports whether host is a multicast DNS name such as "remarkable.local".
func IsMDNSName(host string) bool {
	return strings.HasSuffix(strings.ToLower(strings.TrimSuffix(host, ".")), ".local")
}

// ResolveMDNS looks up the IPv4 address of an mDNS name (e.g. "remarkable.local") by asking the
// local network directly. A program built without cgo can't use the system's Avahi/nss-mdns,
// and Go's own resolver doesn't speak mDNS, so this is how a static binary finds the tablet.
func ResolveMDNS(ctx context.Context, name string) (net.IP, error) {
	query, err := buildMDNSQuery(name)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		return nil, fmt.Errorf("opening a UDP socket for mDNS: %w", err)
	}
	defer conn.Close()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(time.Second)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("setting mDNS deadline: %w", err)
	}
	// A query from a port other than 5353 is a "legacy unicast" query (RFC 6762, 6.7):
	// the owner of the name answers straight back to this socket.
	if _, err := conn.WriteTo(query, mdnsGroup); err != nil {
		return nil, fmt.Errorf("sending mDNS query for %s: %w", name, err)
	}
	buf := make([]byte, 1500)
	for {
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			return nil, fmt.Errorf("no mDNS answer for %s: %w", name, err)
		}
		if ip, found := parseMDNSAnswer(buf[:n], name); found {
			return ip, nil
		}
	}
}

// buildMDNSQuery encodes a question for the A record of name.
func buildMDNSQuery(name string) ([]byte, error) {
	qname, err := dnsmessage.NewName(strings.TrimSuffix(name, ".") + ".")
	if err != nil {
		return nil, fmt.Errorf("invalid mDNS name %q: %w", name, err)
	}
	msg := dnsmessage.Message{
		Questions: []dnsmessage.Question{{Name: qname, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}},
	}
	query, err := msg.Pack()
	if err != nil {
		return nil, fmt.Errorf("encoding mDNS query for %s: %w", name, err)
	}
	return query, nil
}

// parseMDNSAnswer returns the first A record for name in an mDNS response.
func parseMDNSAnswer(packet []byte, name string) (net.IP, bool) {
	var msg dnsmessage.Message
	if err := msg.Unpack(packet); err != nil || !msg.Response {
		return nil, false
	}
	want := strings.ToLower(strings.TrimSuffix(name, ".") + ".")
	for _, answer := range append(msg.Answers, msg.Additionals...) {
		a, ok := answer.Body.(*dnsmessage.AResource)
		if !ok || strings.ToLower(answer.Header.Name.String()) != want {
			continue
		}
		return net.IP(a.A[:]), true
	}
	return nil, false
}
