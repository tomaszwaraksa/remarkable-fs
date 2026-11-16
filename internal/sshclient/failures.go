package sshclient

import (
	"errors"
	"strings"

	"remarkable-fs/internal/logger"
)

// failureKind sorts connection failures by what the user has to do about them.
type failureKind string

const (
	failureUnreachable     failureKind = "unreachable"      // asleep, away, or another network: just wait
	failureRefused         failureKind = "refused"          // answers, but SSH is off (e.g. SSH over Wi-Fi)
	failureRejectedKey     failureKind = "rejected-key"     // answers, but doesn't accept this computer's key
	failureIdentityChanged failureKind = "identity-changed" // answers with a different host key
)

// classifyFailure tells which kind of failure err is.
func classifyFailure(err error) failureKind {
	var changed *IdentityChangedError
	switch {
	case errors.As(err, &changed):
		return failureIdentityChanged
	case isAuthError(err):
		return failureRejectedKey
	case strings.Contains(strings.ToLower(err.Error()), "connection refused"):
		return failureRefused
	default:
		return failureUnreachable
	}
}

// noteFailure logs a failure the user has to act on, once per host until the state changes or a
// connection succeeds, so retry loops don't flood the log. Unreachable hosts aren't logged here.
func (c *Client) noteFailure(host string, err error) {
	kind := classifyFailure(err)
	if kind == failureUnreachable {
		return
	}
	c.noteMu.Lock()
	defer c.noteMu.Unlock()
	if c.lastFailure[host] == kind {
		return
	}
	c.lastFailure[host] = kind
	logger.Error("%s", failureMessage(kind, host, c.cfg.KeyPath))
}

// hasLoggedFailure reports whether a failure the user has to act on was logged since the last
// successful connection; the "probably asleep" hint would then be misleading.
func (c *Client) hasLoggedFailure() bool {
	c.noteMu.Lock()
	defer c.noteMu.Unlock()
	return len(c.lastFailure) > 0
}

// clearFailures forgets the logged failures after a successful connection.
func (c *Client) clearFailures() {
	c.noteMu.Lock()
	defer c.noteMu.Unlock()
	clear(c.lastFailure)
}

// failureMessage is the log line for a failure; the installer's --status looks for the phrases
// "tablet identity changed", "rejected the key" and "refused the connection".
func failureMessage(kind failureKind, host, keyPath string) string {
	key := "this computer's key"
	if keyPath != "" {
		key = "the key " + keyPath
	}
	switch kind {
	case failureIdentityChanged:
		return "Refusing to connect to " + host + ": tablet identity changed. If you updated or reset the tablet, " +
			"connect it with the USB cable once (or run ./install.sh --reconfigure)."
	case failureRejectedKey:
		return "The tablet at " + host + " rejected the key: it doesn't accept " + key + " (was the tablet reset?). " +
			"Connect the USB cable and run ./install.sh --reconfigure."
	case failureRefused:
		return "The tablet at " + host + " refused the connection: SSH may be switched off there " +
			"(over Wi-Fi it is off by default on firmware 3.22+). Connect the USB cable once, so remarkable-fs can switch it on."
	default:
		return "The tablet at " + host + " can't be reached."
	}
}
