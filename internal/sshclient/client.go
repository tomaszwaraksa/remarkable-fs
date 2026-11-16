package sshclient

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"

	"remarkable-fs/internal/discovery"
	"remarkable-fs/internal/logger"
	"remarkable-fs/internal/paths"
)

// Client wraps SSH and SFTP connections to the reMarkable tablet with automatic reconnection.
type Client struct {
	mu             sync.RWMutex
	execMu         sync.RWMutex
	restartMu      sync.Mutex
	restartTimer   *time.Timer
	cfg            Config
	candidateHosts []string
	connectedHost  string
	sshClient      *ssh.Client
	sftpClient     *sftp.Client
	closed         bool
	hostKeys       *hostKeyStore // nil: host keys aren't checked (only without KnownHostsPath)
	tabletLock     tabletLock    // shared with other computers: see LockTablet

	noteMu      sync.Mutex
	lastFailure map[string]failureKind // per host, the failure last logged; cleared on success
	resolved    map[string]string      // per .local name, the address last logged
}

// Config contains SSH connection parameters.
type Config struct {
	Host          string
	FallbackHosts []string
	Port          int
	User          string
	KeyPath       string
	Password      string
	Timeout       time.Duration
	// KnownHostsPath is the known_hosts file the tablet's identity is remembered in. Empty means
	// host keys aren't checked at all; the daemon always sets it.
	KnownHostsPath string
}

// maxRetryWait caps the pause between connection attempts while the tablet can't be reached.
const maxRetryWait = 30 * time.Second

// New prepares a client for the tablet without connecting yet; WaitForConnection connects it.
func New(cfg Config) *Client {
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	if cfg.User == "" {
		cfg.User = "root"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	client := &Client{
		cfg:            cfg,
		candidateHosts: candidateHosts(cfg),
		lastFailure:    make(map[string]failureKind),
		resolved:       make(map[string]string),
	}
	if cfg.KnownHostsPath != "" {
		client.hostKeys = &hostKeyStore{path: cfg.KnownHostsPath}
	}
	return client
}

// candidateHosts lists the configured host and the fallback hosts, without blanks or duplicates.
func candidateHosts(cfg Config) []string {
	var candidates []string
	seen := make(map[string]bool)
	for _, h := range append([]string{cfg.Host}, cfg.FallbackHosts...) {
		h = strings.TrimSpace(h)
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		candidates = append(candidates, h)
	}
	return candidates
}

// WaitForConnection tries the candidate hosts until one answers or ctx ends. It never gives up
// on its own: the tablet may sleep for days. The pause between rounds grows up to maxRetryWait.
func (c *Client) WaitForConnection(ctx context.Context) error {
	wait := 2 * time.Second
	for round := 0; ; round++ {
		if round == 0 {
			logger.Info("Connecting to reMarkable tablet (%s) as %s...", strings.Join(c.candidateHosts, ", "), c.cfg.User)
		}
		if c.tryCandidates() {
			return nil
		}
		if round == 0 && ctx.Err() == nil && !c.hasLoggedFailure() {
			logger.Info("Tablet is not reachable yet (it may be asleep). remarkable-fs keeps trying; tap the power button to wake it.")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, maxRetryWait)
	}
}

// tryCandidates makes one connection attempt to each candidate host, and keeps the first that works.
func (c *Client) tryCandidates() bool {
	dialTimeout := min(c.cfg.Timeout, 3*time.Second)
	for _, host := range c.candidateHosts {
		sshConn, sftpConn, err := c.dialHostWithTimeout(host, dialTimeout)
		if err != nil {
			continue
		}
		c.mu.Lock()
		c.sshClient = sshConn
		c.sftpClient = sftpConn
		c.connectedHost = host
		c.mu.Unlock()

		addr := hostPort(host, c.cfg.Port)
		logger.Connection("Connected SSH session to %s (user: %s)", addr, c.cfg.User)
		logger.Connection("SFTP subsystem initialized successfully on %s", addr)
		return true
	}
	return false
}

// dialHostWithTimeout connects SSH and initializes SFTP with a specific timeout. Failures that
// need the user (changed identity, rejected key) are logged once per state, see noteFailure.
func (c *Client) dialHostWithTimeout(host string, timeout time.Duration) (*ssh.Client, *sftp.Client, error) {
	sshConn, err := c.sshDial(host, timeout, buildAuthMethods(c.cfg))
	if err != nil && isAuthError(err) && c.cfg.Password == "" && term.IsTerminal(int(syscall.Stdin)) {
		if password := promptRootPassword(c.cfg.User, host); password != "" {
			c.cfg.Password = password
			sshConn, err = c.sshDial(host, timeout, []ssh.AuthMethod{ssh.Password(password)})
		}
	}
	if err != nil {
		c.noteFailure(host, err)
		return nil, nil, err
	}

	sftpConn, err := sftp.NewClient(sshConn)
	if err != nil {
		_ = sshConn.Close()
		return nil, nil, fmt.Errorf("failed to initialize SFTP subsystem: %w", err)
	}
	c.clearFailures()
	return sshConn, sftpConn, nil
}

// sshDial opens the TCP connection (resolving .local names itself) and runs the SSH handshake,
// checking the tablet's identity. The whole handshake is bounded by timeout plus a grace period.
func (c *Client) sshDial(host string, timeout time.Duration, auth []ssh.AuthMethod) (*ssh.Client, error) {
	addr := hostPort(c.resolveHost(host, timeout), c.cfg.Port)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	hostKeyCallback := ssh.InsecureIgnoreHostKey()
	var hostKeyAlgorithms []string
	if c.hostKeys != nil {
		isUSB := isUSBConnection(conn)
		hostKeyCallback = c.hostKeys.callback(isUSB)
		hostKeyAlgorithms = c.hostKeys.algorithms(isUSB)
	}
	clientConfig := &ssh.ClientConfig{
		User:              c.cfg.User,
		Auth:              auth,
		HostKeyCallback:   hostKeyCallback,
		HostKeyAlgorithms: hostKeyAlgorithms,
		Timeout:           timeout,
	}
	// Errors from SetDeadline only happen on a closed connection, which the handshake reports anyway.
	_ = conn.SetDeadline(time.Now().Add(timeout + 10*time.Second))
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, hostPort(host, c.cfg.Port), clientConfig)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(sshConn, chans, reqs), nil
}

// resolveHost returns the address to dial for host. Names ending in .local are looked up with
// mDNS when the system resolver can't (a static build can't use Avahi/nss-mdns).
func (c *Client) resolveHost(host string, timeout time.Duration) string {
	if !discovery.IsMDNSName(host) {
		return host
	}
	ctx, cancel := context.WithTimeout(context.Background(), min(timeout, 1500*time.Millisecond))
	defer cancel()
	if addrs, err := net.DefaultResolver.LookupHost(ctx, host); err == nil && len(addrs) > 0 {
		return host
	}
	ip, err := discovery.ResolveMDNS(ctx, host)
	if err != nil {
		return host // dialing then fails like any unreachable host
	}
	c.noteResolved(host, ip.String())
	return ip.String()
}

// noteResolved logs an mDNS lookup result when it differs from the one logged before.
func (c *Client) noteResolved(host, ip string) {
	c.noteMu.Lock()
	defer c.noteMu.Unlock()
	if c.resolved[host] == ip {
		return
	}
	c.resolved[host] = ip
	logger.Info("Found %s at %s (mDNS)", host, ip)
}

// promptRootPassword asks for the tablet's root password on the terminal; "" if none was typed.
func promptRootPassword(user, host string) string {
	fmt.Printf("\nSSH key authentication failed for %s@%s.\n", user, host)
	fmt.Printf("Enter reMarkable root password (from Settings > Help > About > Copyrights and licenses): ")
	pwBytes, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return ""
	}
	return string(pwBytes)
}

// EnsureConnected checks if the connection is alive, and automatically reconnects if dead.
func (c *Client) EnsureConnected() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("client is closed")
	}

	if c.isAliveLocked() {
		return nil
	}
	if c.connectedHost == "" {
		// Never connected yet: WaitForConnection is still trying in the background.
		return fmt.Errorf("not connected to the tablet yet")
	}

	return c.reconnectLocked(20 * time.Second)
}

// isAliveLocked checks if the active SFTP session is responsive without blocking indefinitely.
func (c *Client) isAliveLocked() bool {
	if c.sshClient == nil || c.sftpClient == nil {
		return false
	}
	done := make(chan bool, 1)
	go func(s *sftp.Client) {
		_, err := s.Getwd()
		done <- (err == nil)
	}(c.sftpClient)

	select {
	case alive := <-done:
		return alive
	case <-time.After(1500 * time.Millisecond):
		return false
	}
}

// IsConnected returns whether the client currently has an active connection (non-blocking).
func (c *Client) IsConnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sshClient != nil && c.sftpClient != nil && !c.closed
}

// reconnectLocked closes dead sessions and retries connecting until successful or timeout.
func (c *Client) reconnectLocked(maxWait time.Duration) error {
	oldAddr := hostPort(c.connectedHost, c.cfg.Port)
	logger.Disconnection("Connection to reMarkable at %s lost (tablet may have gone to sleep). Reconnecting...", oldAddr)

	c.closeSessionLocked()

	startTime := time.Now()
	retryInterval := 500 * time.Millisecond

	// Order candidates: try previously connected host first if available
	hosts := make([]string, 0, len(c.candidateHosts))
	if c.connectedHost != "" {
		hosts = append(hosts, c.connectedHost)
	}
	for _, h := range c.candidateHosts {
		if h != c.connectedHost {
			hosts = append(hosts, h)
		}
	}

	for time.Since(startTime) < maxWait {
		for _, host := range hosts {
			sshConn, sftpConn, err := c.dialHostWithTimeout(host, 2*time.Second)
			if err == nil {
				c.sshClient = sshConn
				c.sftpClient = sftpConn
				c.connectedHost = host

				newAddr := hostPort(host, c.cfg.Port)
				logger.Connection("Successfully reconnected to reMarkable at %s", newAddr)
				return nil
			}
		}

		time.Sleep(retryInterval)
		if retryInterval < 2*time.Second {
			retryInterval += 500 * time.Millisecond
		}
	}

	return fmt.Errorf("reconnection timed out after %v", maxWait)
}

func (c *Client) closeSessionLocked() {
	if c.sftpClient != nil {
		_ = c.sftpClient.Close()
		c.sftpClient = nil
	}
	if c.sshClient != nil {
		_ = c.sshClient.Close()
		c.sshClient = nil
	}
}

// Close closes SFTP and SSH connections and logs the disconnection.
func (c *Client) Close() error {
	c.restartMu.Lock()
	if c.restartTimer != nil {
		c.restartTimer.Stop()
		c.restartTimer = nil
	}
	c.restartMu.Unlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	if c.connectedHost != "" {
		logger.Disconnection("Closing SFTP and SSH connection to %s", hostPort(c.connectedHost, c.cfg.Port))
	}
	c.closeSessionLocked()
	return nil
}

// executeCommandRaw runs an SSH command over the current session without retrying or queue locking.
func (c *Client) executeCommandRaw(cmd string) (string, error) {
	if err := c.EnsureConnected(); err != nil {
		return "", err
	}

	c.mu.RLock()
	sshConn := c.sshClient
	c.mu.RUnlock()

	if sshConn == nil {
		return "", fmt.Errorf("SSH client is not connected")
	}

	session, err := sshConn.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	out, err := session.CombinedOutput(cmd)
	return string(out), err
}

// executeCommandWithRetry runs an SSH command, automatically reconnecting if a connection drop is encountered.
func (c *Client) executeCommandWithRetry(cmd string, maxRetries int) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		out, err := c.executeCommandRaw(cmd)
		if err == nil {
			return out, nil
		}
		if !isConnectionDropped(err) {
			return out, err
		}

		lastErr = err
		logger.Info("SSH command \"%s\" encountered connection drop (%v), reconnecting (attempt %d/%d)...", cmd, err, attempt+1, maxRetries)
		c.mu.Lock()
		c.closeSessionLocked()
		c.mu.Unlock()

		if recErr := c.EnsureConnected(); recErr != nil {
			return out, fmt.Errorf("failed to reconnect during command retry: %w (original: %v)", recErr, lastErr)
		}
	}
	return "", lastErr
}

// ExecuteCommand runs a command on the remote host via an SSH session with queue protection and automatic retry.
func (c *Client) ExecuteCommand(cmd string) (string, error) {
	c.execMu.RLock()
	defer c.execMu.RUnlock()

	return c.executeCommandWithRetry(cmd, 2)
}

// EnableWiFiSSH switches on SSH over Wi-Fi on the tablet. Firmware 3.22+ ships with it off;
// older firmware has no rm-ssh-over-wlan command and always allows it.
func (c *Client) EnableWiFiSSH() {
	logger.Info("Switching on SSH over Wi-Fi on the tablet (set enable_wifi_ssh: false in the configuration to leave it alone)")
	out, err := c.ExecuteCommand("if command -v rm-ssh-over-wlan >/dev/null 2>&1; then rm-ssh-over-wlan on 2>&1; fi; true")
	if err != nil {
		logger.Warn("Could not switch on SSH over Wi-Fi on the tablet: %v", err)
		return
	}
	if msg := strings.TrimSpace(out); msg != "" {
		logger.Info("rm-ssh-over-wlan: %s", msg)
	}
}

// InstallPublicKey makes sure the tablet's authorized_keys holds this computer's public key.
// With ssh_key configured only that key is installed, otherwise the first standard key found.
func (c *Client) InstallPublicKey() {
	for _, pubPath := range publicKeyCandidates(c.cfg.KeyPath) {
		pubData, err := os.ReadFile(pubPath)
		if err != nil {
			if c.cfg.KeyPath != "" {
				logger.Warn("Can't make sure the tablet knows this computer's key: %s is missing or unreadable (%v)", pubPath, err)
			}
			continue
		}
		pubKey := strings.TrimSpace(string(pubData))
		if pubKey == "" {
			continue
		}
		if _, err := c.ExecuteCommand(authorizeKeyCommand(pubKey)); err != nil {
			logger.Warn("Could not add public key %s to the tablet: %v", pubPath, err)
			continue
		}
		logger.Info("Public key %s is in the tablet's ~/.ssh/authorized_keys", pubPath)
		return
	}
}

// authorizeKeyCommand returns the remote shell command that adds pubKey to the logged-in user's
// ~/.ssh/authorized_keys, unless that exact line is already there.
func authorizeKeyCommand(pubKey string) string {
	quoted := shellQuote(pubKey)
	return `mkdir -p "$HOME/.ssh" && chmod 700 "$HOME/.ssh" && touch "$HOME/.ssh/authorized_keys" && ` +
		`chmod 600 "$HOME/.ssh/authorized_keys" && ` +
		`{ grep -qxF ` + quoted + ` "$HOME/.ssh/authorized_keys" || echo ` + quoted + ` >> "$HOME/.ssh/authorized_keys"; }`
}

// publicKeyCandidates lists the public key files to install: the configured key's .pub only,
// or, without a configured key, the standard ~/.ssh/id_ed25519.pub and ~/.ssh/id_rsa.pub.
func publicKeyCandidates(keyPath string) []string {
	if keyPath != "" {
		return []string{paths.ExpandHome(keyPath) + ".pub"}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".ssh", "id_ed25519.pub"),
		filepath.Join(home, ".ssh", "id_rsa.pub"),
	}
}

// hostPort joins host and port for dialing and logs; IPv6 hosts get brackets ([::1]:22).
func hostPort(host string, port int) string {
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// shellQuote wraps s in single quotes for a POSIX shell, so it is passed on literally.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// undoSleepBlockersCommand undoes only what old remarkable-fs versions did to keep the tablet awake:
// a masked suspend.target (a link to /dev/null), their systemd-inhibit (recognisable by its --why
// text) and their input heartbeat loop. Anything else, e.g. an inhibitor held by a software update,
// is left alone. The tablet has pgrep but no pkill; the [r] and [/] brackets keep pgrep from
// matching this shell's own command line.
const undoSleepBlockersCommand = `if [ "$(readlink /etc/systemd/system/suspend.target)" = /dev/null ]; then systemctl unmask suspend.target >/dev/null 2>&1 && echo unmasked-suspend; fi; ` +
	`p=$(pgrep -f '[r]emarkable-fs connection active'); [ -n "$p" ] && kill $p 2>/dev/null && echo stopped-inhibitor; ` +
	`p=$(pgrep -f '[/]dev/input/event0 2>/dev/null; sleep 60'); [ -n "$p" ] && kill $p 2>/dev/null && echo stopped-heartbeat; true`

// EnsureSleepUninhibited removes sleep blockers left behind by old remarkable-fs versions, so the
// tablet can sleep normally. It touches nothing it didn't create.
func (c *Client) EnsureSleepUninhibited() {
	out, err := c.ExecuteCommand(undoSleepBlockersCommand)
	if err != nil {
		logger.Warn("Could not check the tablet for sleep blockers left by older versions: %v", err)
		return
	}
	if done := strings.Fields(out); len(done) > 0 {
		logger.Info("Removed sleep blockers left by an older remarkable-fs version: %s", strings.Join(done, ", "))
	}
}

// ScheduleXochitlRestart debounces UI reload requests so that sequential operations (like deleting or uploading multiple files)
// only trigger a single xochitl restart after a quiet period of inactivity, without blocking or causing connection drops.
func (c *Client) ScheduleXochitlRestart() {
	c.restartMu.Lock()
	defer c.restartMu.Unlock()

	if c.closed {
		return
	}

	if c.restartTimer != nil {
		c.restartTimer.Stop()
	}

	c.restartTimer = time.AfterFunc(2500*time.Millisecond, func() {
		c.triggerXochitlRestart()
	})
}

func (c *Client) triggerXochitlRestart() {
	c.restartMu.Lock()
	if c.closed {
		c.restartMu.Unlock()
		return
	}
	c.restartMu.Unlock()

	// No other computer changes documents or restarts the screen meanwhile (taken before execMu,
	// as everywhere else).
	defer c.LockTablet()()

	// Acquire exclusive execution lock so no mutating commands execute during the service restart window
	c.execMu.Lock()
	defer c.execMu.Unlock()

	logger.Info("Debounce window closed. Refreshing reMarkable UI (restarting xochitl)...")
	_, err := c.executeCommandWithRetry("touch /home/root/.local/share/remarkable/xochitl && systemctl reset-failed xochitl.service && systemctl --no-block restart xochitl", 2)
	if err != nil {
		logger.Error("Failed to trigger debounced xochitl restart: %v", err)
	} else {
		logger.Info("reMarkable UI refresh signal sent successfully")
	}

	// Give the service a brief 1.5s grace period to settle
	time.Sleep(1500 * time.Millisecond)
}

// Open opens a remote file for reading, automatically reconnecting if connection was lost.
func (c *Client) Open(path string) (*sftp.File, error) {
	if err := c.EnsureConnected(); err != nil {
		return nil, err
	}

	c.mu.RLock()
	sftpConn := c.sftpClient
	c.mu.RUnlock()

	if sftpConn == nil {
		return nil, fmt.Errorf("SFTP client is not connected")
	}

	f, err := sftpConn.Open(path)
	if err != nil && isConnectionDropped(err) {
		// Retry once after forced reconnect
		logger.Info("SFTP Open encountered connection drop, retrying after reconnect...")
		if recErr := c.EnsureConnected(); recErr == nil {
			c.mu.RLock()
			sftpConn = c.sftpClient
			c.mu.RUnlock()
			if sftpConn != nil {
				return sftpConn.Open(path)
			}
		}
	}
	return f, err
}

// Create creates or truncates a remote file for writing via SFTP.
func (c *Client) Create(path string) (*sftp.File, error) {
	if err := c.EnsureConnected(); err != nil {
		return nil, err
	}

	c.mu.RLock()
	sftpConn := c.sftpClient
	c.mu.RUnlock()

	if sftpConn == nil {
		return nil, fmt.Errorf("SFTP client is not connected")
	}

	f, err := sftpConn.Create(path)
	if err != nil && isConnectionDropped(err) {
		if recErr := c.EnsureConnected(); recErr == nil {
			c.mu.RLock()
			sftpConn = c.sftpClient
			c.mu.RUnlock()
			if sftpConn != nil {
				return sftpConn.Create(path)
			}
		}
	}
	return f, err
}

// WriteFile writes bytes to a remote file via SFTP with queue protection and automatic retry.
func (c *Client) WriteFile(path string, data []byte) error {
	c.execMu.RLock()
	defer c.execMu.RUnlock()

	var lastErr error
	for attempt := 0; attempt <= 2; attempt++ {
		f, err := c.Create(path)
		if err != nil {
			if isConnectionDropped(err) {
				lastErr = err
				_ = c.EnsureConnected()
				continue
			}
			return err
		}

		_, err = f.Write(data)
		_ = f.Close()
		if err == nil {
			return nil
		}
		if !isConnectionDropped(err) {
			return err
		}
		lastErr = err
		_ = c.EnsureConnected()
	}
	return lastErr
}

// Stat retrieves file info for a remote path.
func (c *Client) Stat(path string) (os.FileInfo, error) {
	if err := c.EnsureConnected(); err != nil {
		return nil, err
	}

	c.mu.RLock()
	sftpConn := c.sftpClient
	c.mu.RUnlock()

	if sftpConn == nil {
		return nil, fmt.Errorf("SFTP client is not connected")
	}
	return sftpConn.Stat(path)
}

// ReadDir lists contents of a remote directory.
func (c *Client) ReadDir(path string) ([]os.FileInfo, error) {
	if err := c.EnsureConnected(); err != nil {
		return nil, err
	}

	c.mu.RLock()
	sftpConn := c.sftpClient
	c.mu.RUnlock()

	if sftpConn == nil {
		return nil, fmt.Errorf("SFTP client is not connected")
	}
	return sftpConn.ReadDir(path)
}

// ReadFile reads the full contents of a remote file.
func (c *Client) ReadFile(path string) ([]byte, error) {
	file, err := c.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}

	buf := make([]byte, stat.Size())
	_, err = file.Read(buf)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf, nil
}

// ConnectedHost returns the currently connected host.
func (c *Client) ConnectedHost() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.connectedHost
}

func isConnectionDropped(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "eof") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "use of closed network connection")
}

// buildAuthMethods constructs the list of available authentication methods. The configured key
// comes first; the standard keys in ~/.ssh and ssh-agent keys are a quiet fallback.
func buildAuthMethods(cfg Config) []ssh.AuthMethod {
	var signers []ssh.Signer

	// 1. Explicit key from configuration or CLI flags
	if cfg.KeyPath != "" {
		if s := loadSigner(paths.ExpandHome(cfg.KeyPath), true); s != nil {
			signers = append(signers, s)
		}
	}

	// 2. Standard user identity keys from ~/.ssh/
	home, err := os.UserHomeDir()
	if err == nil {
		defaultKeyFiles := []string{
			filepath.Join(home, ".ssh", "id_ed25519"),
			filepath.Join(home, ".ssh", "id_rsa"),
			filepath.Join(home, ".ssh", "id_ecdsa"),
		}
		for _, keyFile := range defaultKeyFiles {
			if s := loadSigner(keyFile, false); s != nil {
				signers = append(signers, s)
			}
		}
	}

	// 3. SSH Agent (with timeout and error safety)
	if authSock := os.Getenv("SSH_AUTH_SOCK"); authSock != "" {
		if agentConn, err := net.DialTimeout("unix", authSock, 500*time.Millisecond); err == nil {
			agentClient := agent.NewClient(agentConn)
			// Probe agent to verify it's responsive and has keys before adding
			if agentSigners, err := agentClient.Signers(); err == nil && len(agentSigners) > 0 {
				signers = append(signers, agentSigners...)
			} else {
				_ = agentConn.Close()
			}
		}
	}

	var methods []ssh.AuthMethod
	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}

	if cfg.Password != "" {
		methods = append(methods, ssh.Password(cfg.Password))
	}

	return methods
}

// skippedKeys remembers keys already reported as unusable, so reconnect loops don't repeat the warning.
var skippedKeys sync.Map

// loadSigner reads a private key; nil if it can't be used. Only the configured key (isConfigured)
// is reported when unusable: the standard keys are just tried in passing.
func loadSigner(path string, isConfigured bool) ssh.Signer {
	data, err := os.ReadFile(path)
	if err != nil {
		if isConfigured {
			warnSkippedKey(path, err)
		}
		return nil
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		if isConfigured {
			warnSkippedKey(path, err)
		}
		return nil
	}
	return signer
}

// warnSkippedKey logs once per path why a configured key can't be used.
func warnSkippedKey(path string, err error) {
	if _, reported := skippedKeys.LoadOrStore(path, true); reported {
		return
	}
	var passphraseMissing *ssh.PassphraseMissingError
	if errors.As(err, &passphraseMissing) {
		logger.Warn("Skipping SSH key %s: it is protected by a passphrase, which remarkable-fs can't type. Load it into ssh-agent, or use a key without a passphrase.", path)
		return
	}
	logger.Warn("Skipping SSH key %s: %v", path, err)
}

func isAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "authentication failed") ||
		strings.Contains(msg, "permission denied")
}

// PromptPassword interactively prompts the user for the password via terminal.
func PromptPassword(prompt string) (string, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
