package discovery

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"remarkable-fs/internal/logger"
)

// DiscoveredHost represents a discovered reMarkable tablet on the local network.
type DiscoveredHost struct {
	Hostname string
	IP       string
	Source   string
}

// Discover finds reMarkable devices on the local network using mDNS, standard DNS, and local neighbor scanning.
func Discover(ctx context.Context, timeout time.Duration) []DiscoveredHost {
	logger.Info("Starting auto-detection of reMarkable tablet on the local network...")
	var results []DiscoveredHost
	seen := make(map[string]bool)

	addHost := func(host DiscoveredHost) {
		key := host.IP
		if key == "" {
			key = host.Hostname
		}
		if !seen[key] {
			seen[key] = true
			results = append(results, host)
			logger.Info("Found candidate reMarkable device: %s (%s) via %s", host.Hostname, host.IP, host.Source)
		}
	}

	// 1. Check standard DNS / mDNS resolution for remarkable.local
	resolveNames := []string{"remarkable.local", "remarkable"}
	for _, name := range resolveNames {
		ips, err := net.LookupHost(name)
		if err == nil {
			for _, ip := range ips {
				addHost(DiscoveredHost{
					Hostname: name,
					IP:       ip,
					Source:   "DNS/mDNS lookup",
				})
			}
		}
	}

	// 2. Multicast DNS direct query (UDP 224.0.0.251:5353)
	mdnsHosts := queryMDNS("remarkable.local", 1200*time.Millisecond)
	for _, h := range mdnsHosts {
		addHost(h)
	}

	// 3. Check /proc/net/arp or local neighbor cache
	arpHosts := scanArpCache()
	for _, h := range arpHosts {
		addHost(h)
	}

	// 4. Check standard reMarkable USB network interface (10.11.99.1)
	if testTCPPort("10.11.99.1", 22, 400*time.Millisecond) {
		addHost(DiscoveredHost{
			Hostname: "10.11.99.1",
			IP:       "10.11.99.1",
			Source:   "USB Interface",
		})
	}

	return results
}

func testTCPPort(host string, port int, timeout time.Duration) bool {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err == nil {
		_ = conn.Close()
		return true
	}
	return false
}

// queryMDNS sends a raw unicast/multicast DNS query to 224.0.0.251:5353.
func queryMDNS(domain string, timeout time.Duration) []DiscoveredHost {
	var hosts []DiscoveredHost

	// Build standard DNS query
	var buf bytes.Buffer
	// Transaction ID (0), Flags (0)
	buf.Write([]byte{0x00, 0x00, 0x00, 0x00})
	// Questions: 1, Answers: 0, Authority: 0, Additional: 0
	buf.Write([]byte{0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})

	// QNAME
	labels := strings.Split(domain, ".")
	for _, label := range labels {
		if len(label) > 0 {
			buf.WriteByte(byte(len(label)))
			buf.WriteString(label)
		}
	}
	buf.WriteByte(0x00) // Null terminator

	// QTYPE: ANY (0x00ff) or A (0x0001)
	binary.Write(&buf, binary.BigEndian, uint16(1)) // Type A
	// QCLASS: IN (0x0001)
	binary.Write(&buf, binary.BigEndian, uint16(1))

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return hosts
	}
	defer conn.Close()

	dest := &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353}
	_, _ = conn.WriteTo(buf.Bytes(), dest)

	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	recvBuf := make([]byte, 2048)

	for {
		n, remoteAddr, err := conn.ReadFrom(recvBuf)
		if err != nil {
			break
		}
		if n > 0 {
			ip := ""
			if udpAddr, ok := remoteAddr.(*net.UDPAddr); ok {
				ip = udpAddr.IP.String()
			}
			hosts = append(hosts, DiscoveredHost{
				Hostname: domain,
				IP:       ip,
				Source:   "mDNS Multicast",
			})
		}
	}

	return hosts
}

// scanArpCache checks /proc/net/arp for any host with hostname or MAC related to reMarkable
func scanArpCache() []DiscoveredHost {
	var hosts []DiscoveredHost
	file, err := os.Open("/proc/net/arp")
	if err != nil {
		return hosts
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	// Skip header
	if scanner.Scan() {
		_ = scanner.Text()
	}

	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 4 {
			ip := fields[0]
			mac := strings.ToLower(fields[3])
			// Check if IP resolves to remarkable or known OUI
			if mac != "00:00:00:00:00:00" {
				names, err := net.LookupAddr(ip)
				if err == nil {
					for _, name := range names {
						if strings.Contains(strings.ToLower(name), "remarkable") {
							hosts = append(hosts, DiscoveredHost{
								Hostname: strings.TrimSuffix(name, "."),
								IP:       ip,
								Source:   "ARP Cache",
							})
						}
					}
				}
			}
		}
	}
	return hosts
}

// PromptUserSelection prompts the user in the CLI to choose or confirm a discovered host.
func PromptUserSelection(candidates []DiscoveredHost) (string, error) {
	reader := bufio.NewReader(os.Stdin)

	if len(candidates) == 1 {
		c := candidates[0]
		target := c.Hostname
		if target == "" {
			target = c.IP
		}
		fmt.Printf("\n[remarkable-fs] Discovered reMarkable tablet at %s (%s)\n", c.Hostname, c.IP)
		fmt.Printf("Use this host? [Y/n]: ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "y") || strings.EqualFold(line, "yes") {
			return target, nil
		}
	} else if len(candidates) > 1 {
		fmt.Println("\n[remarkable-fs] Multiple candidate reMarkable tablets found on local network:")
		for i, c := range candidates {
			fmt.Printf("  [%d] %s (%s) [%s]\n", i+1, c.Hostname, c.IP, c.Source)
		}
		for {
			fmt.Printf("Select host [1-%d] or enter custom hostname/IP: ", len(candidates))
			line, _ := reader.ReadString('\n')
			line = strings.TrimSpace(line)
			var idx int
			if _, err := fmt.Sscanf(line, "%d", &idx); err == nil && idx >= 1 && idx <= len(candidates) {
				chosen := candidates[idx-1]
				if chosen.Hostname != "" {
					return chosen.Hostname, nil
				}
				return chosen.IP, nil
			}
			if line != "" {
				return line, nil
			}
		}
	}

	// No candidates found or rejected
	fmt.Printf("\n[remarkable-fs] No reMarkable tablets could be automatically detected on your local network.\n")
	fmt.Printf("Please enter your reMarkable tablet's hostname or IP address (e.g. remarkable.local or 192.168.x.x): ")
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read user input: %w", err)
	}
	target := strings.TrimSpace(line)
	if target == "" {
		return "", fmt.Errorf("no host specified")
	}
	return target, nil
}
