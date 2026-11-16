package discovery

import (
	"net"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

func TestIsMDNSName(t *testing.T) {
	cases := map[string]bool{
		"remarkable.local":  true,
		"Remarkable.LOCAL.": true,
		"10.11.99.1":        false,
		"remarkable":        false,
		"localhost":         false,
		"example.com":       false,
	}
	for in, want := range cases {
		if got := IsMDNSName(in); got != want {
			t.Errorf("IsMDNSName(%q) = %v, want %v", in, got, want)
		}
	}
}

func mdnsResponse(t *testing.T, name string, ip [4]byte) []byte {
	t.Helper()
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{Response: true, Authoritative: true},
		Answers: []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{Name: dnsmessage.MustNewName(name), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 120},
			Body:   &dnsmessage.AResource{A: ip},
		}},
	}
	packet, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return packet
}

func TestParseMDNSAnswer(t *testing.T) {
	packet := mdnsResponse(t, "remarkable.local.", [4]byte{192, 168, 68, 88})
	ip, found := parseMDNSAnswer(packet, "remarkable.local")
	if !found || !ip.Equal(net.IPv4(192, 168, 68, 88)) {
		t.Errorf("Expected 192.168.1.50, got %v (found %v)", ip, found)
	}
	if _, found := parseMDNSAnswer(packet, "other.local"); found {
		t.Errorf("An answer for another name must be ignored")
	}
	query, err := buildMDNSQuery("remarkable.local")
	if err != nil {
		t.Fatal(err)
	}
	if _, found := parseMDNSAnswer(query, "remarkable.local"); found {
		t.Errorf("A query (not a response) must be ignored")
	}
	if _, found := parseMDNSAnswer([]byte{1, 2, 3}, "remarkable.local"); found {
		t.Errorf("Garbage must be ignored")
	}
}
