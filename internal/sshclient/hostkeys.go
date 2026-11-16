package sshclient

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"remarkable-fs/internal/logger"
	"remarkable-fs/internal/paths"
)

// HostKeyAlias is the name the tablet's host key is remembered under in known_hosts, whatever
// address it was reached at (USB, Wi-Fi IP or remarkable.local). The installer uses the same
// name (ssh -o HostKeyAlias=remarkable), so both share one entry.
const HostKeyAlias = "remarkable"

// usbTabletIP is the tablet's fixed address on the USB cable network.
var usbTabletIP = net.IPv4(10, 11, 99, 1)

// IdentityChangedError reports a tablet whose host key differs from the remembered one.
type IdentityChangedError struct {
	Fingerprint string
}

func (e *IdentityChangedError) Error() string {
	return "tablet identity changed: host key " + e.Fingerprint + " is not the remembered one"
}

// hostKeyStore checks the tablet's host key against a known_hosts file: the first key seen is
// remembered, a changed key is accepted only over the USB cable, and refused anywhere else.
type hostKeyStore struct {
	mu   sync.Mutex
	path string
}

// callback returns the ssh.HostKeyCallback for one connection; isUSB says whether that
// connection runs over the tablet's USB cable network.
func (s *hostKeyStore) callback(isUSB bool) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		return s.check(key, isUSB)
	}
}

func (s *hostKeyStore) check(key ssh.PublicKey, isUSB bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	pinned, others, err := s.read()
	if err != nil {
		return err
	}
	id := key.Type() + " " + ssh.FingerprintSHA256(key)
	switch {
	case hasKey(pinned, key):
		return nil
	case len(pinned) == 0:
		if err := s.write(others, key); err != nil {
			return err
		}
		logger.Info("Remembered the tablet's identity (%s)", id)
	case isUSB:
		if err := s.write(others, key); err != nil {
			return err
		}
		logger.Warn("The tablet's identity changed (normal after a software update or reset). Trusting the new one because it is connected by USB cable (%s).", id)
	default:
		return &IdentityChangedError{Fingerprint: id}
	}
	return nil
}

// algorithms returns the host key algorithms to ask the tablet for: those of the remembered
// keys, as OpenSSH does, so a tablet offering several key types shows the remembered one.
// Nil (Go's defaults) when nothing is remembered, or over the USB cable, where a new key of
// another type may be learned.
func (s *hostKeyStore) algorithms(isUSB bool) []string {
	if isUSB {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pinned, _, err := s.read()
	if err != nil {
		return nil
	}
	var algorithms []string
	for _, key := range pinned {
		algorithms = append(algorithms, algorithmsForKeyType(key.Type())...)
	}
	return algorithms
}

// algorithmsForKeyType lists the signature algorithms a host key of keyType can be used with.
func algorithmsForKeyType(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{keyType}
}

// read returns the keys remembered for HostKeyAlias, and all other lines unchanged.
func (s *hostKeyStore) read() (pinned []ssh.PublicKey, others []string, err error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading known tablet identities from %s: %w", s.path, err)
	}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if key := aliasKey(line); key != nil {
			pinned = append(pinned, key)
			continue
		}
		others = append(others, line)
	}
	return pinned, others, nil
}

// aliasKey returns the key of a plain (unmarked) known_hosts line for HostKeyAlias, or nil.
func aliasKey(line string) ssh.PublicKey {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return nil
	}
	marker, hosts, key, _, _, err := ssh.ParseKnownHosts([]byte(trimmed))
	if err != nil || marker != "" {
		return nil
	}
	for _, h := range hosts {
		if h == HostKeyAlias {
			return key
		}
	}
	return nil
}

// write replaces the remembered tablet key with key, keeping the other lines.
func (s *hostKeyStore) write(others []string, key ssh.PublicKey) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return fmt.Errorf("creating the folder for %s: %w", s.path, err)
	}
	var buf bytes.Buffer
	for _, line := range others {
		if strings.TrimSpace(line) != "" {
			buf.WriteString(line + "\n")
		}
	}
	buf.WriteString(knownhosts.Line([]string{HostKeyAlias}, key) + "\n")
	return paths.WriteFileAtomic(s.path, buf.Bytes(), 0600)
}

func hasKey(keys []ssh.PublicKey, key ssh.PublicKey) bool {
	for _, k := range keys {
		if bytes.Equal(k.Marshal(), key.Marshal()) {
			return true
		}
	}
	return false
}

// usbSysfsNet is where Linux lists network interfaces; a variable so tests can fake it.
var usbSysfsNet = "/sys/class/net"

// isUSBConnection reports whether conn goes to the tablet over its USB cable network: the far end
// is 10.11.99.1 and the local end is a USB network adapter. Both are needed, so a Wi-Fi network
// that happens to use 10.11.99.1 can't pass as the cable.
func isUSBConnection(conn net.Conn) bool {
	remote, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok || !remote.IP.Equal(usbTabletIP) {
		return false
	}
	local, ok := conn.LocalAddr().(*net.TCPAddr)
	if !ok {
		return false
	}
	name := interfaceWithIP(local.IP)
	return name != "" && isUSBInterface(usbSysfsNet, name)
}

// interfaceWithIP returns the name of the network interface that has ip, or "".
func interfaceWithIP(ip net.IP) string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipNet, ok := addr.(*net.IPNet); ok && ipNet.IP.Equal(ip) {
				return iface.Name
			}
		}
	}
	return ""
}

// isUSBInterface reports whether the network interface's device sits on a USB bus.
func isUSBInterface(sysfsNet, name string) bool {
	device, err := filepath.EvalSymlinks(filepath.Join(sysfsNet, name, "device"))
	return err == nil && strings.Contains(device, "/usb")
}
