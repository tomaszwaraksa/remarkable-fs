package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func newHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestHostKeyStoreTrustsFirstKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conf", "known_hosts")
	store := &hostKeyStore{path: path}
	key := newHostKey(t)

	if err := store.check(key, false); err != nil {
		t.Fatalf("First key must be accepted: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Expected known_hosts to be written: %v", err)
	}
	if !strings.HasPrefix(string(data), "remarkable ssh-ed25519 ") {
		t.Errorf("Expected an unhashed line for the alias, got %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("Expected mode 0600, got %v", info.Mode().Perm())
	}
}

func TestHostKeyStoreAcceptsRememberedKey(t *testing.T) {
	store := &hostKeyStore{path: filepath.Join(t.TempDir(), "known_hosts")}
	key := newHostKey(t)
	if err := store.check(key, false); err != nil {
		t.Fatal(err)
	}
	if err := store.check(key, false); err != nil {
		t.Errorf("The remembered key must be accepted over Wi-Fi: %v", err)
	}
}

func TestHostKeyStoreRefusesChangedKeyOverWiFi(t *testing.T) {
	store := &hostKeyStore{path: filepath.Join(t.TempDir(), "known_hosts")}
	if err := store.check(newHostKey(t), false); err != nil {
		t.Fatal(err)
	}
	err := store.check(newHostKey(t), false)
	var changed *IdentityChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("Expected IdentityChangedError, got %v", err)
	}
	if !strings.Contains(err.Error(), "tablet identity changed") {
		t.Errorf("Expected the log phrase in the error, got %q", err)
	}
}

func TestHostKeyStoreReplacesChangedKeyOverUSB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	other := "other.example ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl\n"
	if err := os.WriteFile(path, []byte("# my comment\n"+other), 0600); err != nil {
		t.Fatal(err)
	}
	store := &hostKeyStore{path: path}
	oldKey, newKey := newHostKey(t), newHostKey(t)
	if err := store.check(oldKey, false); err != nil {
		t.Fatal(err)
	}
	if err := store.check(newKey, true); err != nil {
		t.Fatalf("A changed key must be accepted over USB: %v", err)
	}
	if err := store.check(newKey, false); err != nil {
		t.Errorf("The new key must now be accepted over Wi-Fi: %v", err)
	}
	if err := store.check(oldKey, false); err == nil {
		t.Errorf("The old key must no longer be accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# my comment\n") || !strings.Contains(string(data), other) {
		t.Errorf("Other lines must be kept, got:\n%s", data)
	}
	if strings.Count(string(data), "remarkable ") != 1 {
		t.Errorf("Expected exactly one line for the tablet, got:\n%s", data)
	}
}

func TestIsUSBInterface(t *testing.T) {
	root := t.TempDir()
	devices := filepath.Join(root, "devices")
	usbDev := filepath.Join(devices, "pci0000:00", "usb1", "1-6", "1-6:2.0")
	pciDev := filepath.Join(devices, "pci0000:00", "0000:05:00.0")
	for _, d := range []string{usbDev, pciDev} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	net := filepath.Join(root, "net")
	for name, target := range map[string]string{"enxusb": usbDev, "wlp5s0": pciDev} {
		if err := os.MkdirAll(filepath.Join(net, name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(net, name, "device")); err != nil {
			t.Fatal(err)
		}
	}
	if !isUSBInterface(net, "enxusb") {
		t.Errorf("Expected the USB adapter to be recognised")
	}
	if isUSBInterface(net, "wlp5s0") || isUSBInterface(net, "missing") {
		t.Errorf("Expected Wi-Fi and missing interfaces not to count as USB")
	}
}

func TestHostKeyStoreAlgorithmsFollowRememberedKeys(t *testing.T) {
	store := &hostKeyStore{path: filepath.Join(t.TempDir(), "known_hosts")}
	if got := store.algorithms(false); got != nil {
		t.Errorf("Expected Go's defaults while nothing is remembered, got %v", got)
	}
	if err := store.check(newHostKey(t), false); err != nil {
		t.Fatal(err)
	}
	if got := store.algorithms(false); len(got) != 1 || got[0] != ssh.KeyAlgoED25519 {
		t.Errorf("Expected only %s once an ed25519 key is remembered, got %v", ssh.KeyAlgoED25519, got)
	}
	if got := store.algorithms(true); got != nil {
		t.Errorf("Expected Go's defaults over USB, where a new key type may be learned, got %v", got)
	}
	if got := algorithmsForKeyType(ssh.KeyAlgoRSA); len(got) != 3 {
		t.Errorf("Expected the three RSA signature algorithms, got %v", got)
	}
}
