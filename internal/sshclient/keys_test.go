package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestPublicKeyCandidates(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}

	configured := publicKeyCandidates("~/.ssh/remarkable_ed25519")
	if len(configured) != 1 || configured[0] != filepath.Join(home, ".ssh", "remarkable_ed25519.pub") {
		t.Errorf("With ssh_key set, expected only its .pub, got %v", configured)
	}

	standard := publicKeyCandidates("")
	want := []string{filepath.Join(home, ".ssh", "id_ed25519.pub"), filepath.Join(home, ".ssh", "id_rsa.pub")}
	if len(standard) != 2 || standard[0] != want[0] || standard[1] != want[1] {
		t.Errorf("Without ssh_key, expected %v, got %v", want, standard)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"ssh-ed25519 AAAA user@host": `'ssh-ed25519 AAAA user@host'`,
		"it's":                       `'it'\''s'`,
		"":                           `''`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestLoadSignerSkipsPassphraseKeys(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()

	plain, err := ssh.MarshalPrivateKey(privateKey, "plain")
	if err != nil {
		t.Fatal(err)
	}
	plainPath := filepath.Join(dir, "plain")
	if err := os.WriteFile(plainPath, pem.EncodeToMemory(plain), 0600); err != nil {
		t.Fatal(err)
	}

	encrypted, err := ssh.MarshalPrivateKeyWithPassphrase(privateKey, "locked", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	lockedPath := filepath.Join(dir, "locked")
	if err := os.WriteFile(lockedPath, pem.EncodeToMemory(encrypted), 0600); err != nil {
		t.Fatal(err)
	}

	if loadSigner(plainPath, true) == nil {
		t.Errorf("Expected a key without passphrase to load")
	}
	if loadSigner(lockedPath, false) != nil {
		t.Errorf("Expected a passphrase-protected key to be skipped")
	}
	if _, reported := skippedKeys.Load(lockedPath); reported {
		t.Errorf("A standard (not configured) key must be skipped quietly")
	}
	if loadSigner(lockedPath, true) != nil {
		t.Errorf("Expected a passphrase-protected configured key to be skipped")
	}
	if _, reported := skippedKeys.Load(lockedPath); !reported {
		t.Errorf("Expected the skipped configured key to be reported once and remembered")
	}
	if loadSigner(filepath.Join(dir, "missing"), false) != nil {
		t.Errorf("Expected a missing key file to be skipped")
	}
}

func TestHostPort(t *testing.T) {
	cases := map[string]string{
		"remarkable.local": "remarkable.local:22",
		"10.11.99.1":       "10.11.99.1:22",
		"fe80::1":          "[fe80::1]:22",
	}
	for host, want := range cases {
		if got := hostPort(host, 22); got != want {
			t.Errorf("hostPort(%q, 22) = %q, want %q", host, got, want)
		}
	}
}

func TestAuthorizeKeyCommandUsesRemoteHome(t *testing.T) {
	cmd := authorizeKeyCommand("ssh-ed25519 AAAA it's me")
	if strings.Contains(cmd, "/home/root") {
		t.Errorf("Expected the remote user's $HOME, not /home/root: %s", cmd)
	}
	if !strings.Contains(cmd, `grep -qxF 'ssh-ed25519 AAAA it'\''s me' "$HOME/.ssh/authorized_keys"`) {
		t.Errorf("Expected a whole-line, quoted check against $HOME/.ssh/authorized_keys: %s", cmd)
	}
}
