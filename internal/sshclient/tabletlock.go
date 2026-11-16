package sshclient

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"remarkable-fs/internal/logger"
)

// TabletLockPath is the lock file on the tablet that every computer running remarkable-fs holds
// while it changes documents or restarts the tablet's screen (xochitl), so two computers never
// do that at the same moment. Reading needs no lock.
const TabletLockPath = "/home/root/.cache/remarkable-fs.lock"

// tabletLockCommand takes the lock with the tablet's flock, says "locked" and keeps it until
// its input closes: when we release it, and also when the connection drops or this computer
// goes away, so a lock can't be left behind.
const tabletLockCommand = "mkdir -p /home/root/.cache && exec flock " + TabletLockPath + " sh -c 'echo locked; exec cat >/dev/null'"

// How long to wait for another computer's change before going ahead without the lock (it may
// have gone to sleep mid-change and the tablet not noticed yet), and when to say we're waiting.
var (
	tabletLockWait   = 60 * time.Second
	tabletLockNotice = 2 * time.Second
)

// tabletLock is this computer's hold on the tablet's lock. Changes made here share it: it
// counts holders, so nested or parallel changes on this computer never wait for each other,
// only for other computers.
type tabletLock struct {
	mu      sync.Mutex
	holders int
	session *ssh.Session // nil while not held, or when going ahead without it
	stdin   io.WriteCloser
}

// LockTablet takes the tablet's change lock and returns the function that releases it. If it
// can't be taken (no connection, a firmware without flock, another computer silent for too
// long), it logs why and goes ahead without it, as before this lock existed.
// Never call it while holding execMu: the restart takes the lock first, then execMu.
func (c *Client) LockTablet() (unlock func()) {
	l := &c.tabletLock
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holders == 0 {
		session, stdin, err := c.acquireTabletLock()
		if err != nil {
			logger.Warn("Changing the tablet without its change lock: %v", err)
		}
		l.session, l.stdin = session, stdin
	}
	l.holders++
	return sync.OnceFunc(func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.holders--
		if l.holders == 0 && l.session != nil {
			_ = l.stdin.Close()
			_ = l.session.Close()
			l.session, l.stdin = nil, nil
		}
	})
}

// acquireTabletLock starts the lock command on the tablet and waits until it holds the lock.
func (c *Client) acquireTabletLock() (*ssh.Session, io.WriteCloser, error) {
	if err := c.EnsureConnected(); err != nil {
		return nil, nil, err
	}
	c.mu.RLock()
	conn := c.sshClient
	c.mu.RUnlock()
	if conn == nil {
		return nil, nil, fmt.Errorf("not connected to the tablet")
	}
	session, err := conn.NewSession()
	if err != nil {
		return nil, nil, err
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, nil, err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		session.Close()
		return nil, nil, err
	}
	stderr := &syncBuffer{}
	session.Stderr = stderr
	if err := session.Start(tabletLockCommand); err != nil {
		session.Close()
		return nil, nil, err
	}

	locked := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && strings.TrimSpace(line) == "locked" {
			locked <- nil
			return
		}
		locked <- fmt.Errorf("the tablet couldn't take it (%s)", strings.TrimSpace(stderr.String()))
	}()

	notice := time.NewTimer(tabletLockNotice)
	defer notice.Stop()
	giveUp := time.NewTimer(tabletLockWait)
	defer giveUp.Stop()
	for {
		select {
		case err := <-locked:
			if err != nil {
				session.Close()
				return nil, nil, err
			}
			return session, stdin, nil
		case <-notice.C:
			logger.Info("Waiting for another computer to finish changing documents on the tablet...")
		case <-giveUp.C:
			session.Close()
			return nil, nil, fmt.Errorf("another computer has been changing documents on the tablet for over %s", tabletLockWait)
		}
	}
}

// syncBuffer is a bytes.Buffer that the SSH session can write while we read it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
