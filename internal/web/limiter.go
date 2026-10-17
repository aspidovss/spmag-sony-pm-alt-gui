package web

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// authLimiter slows down password guessing: after maxFails wrong logins from
// one address within window, that address gets "429 Too Many Requests" for
// block, without even looking at the credentials.
type authLimiter struct {
	mu      sync.Mutex
	entries map[string]*failEntry
	now     func() time.Time

	maxFails int
	window   time.Duration
	block    time.Duration
}

type failEntry struct {
	fails   int
	first   time.Time
	blocked time.Time
}

const maxLimiterEntries = 4096

func newAuthLimiter() *authLimiter {
	return &authLimiter{entries: map[string]*failEntry{}, now: time.Now, maxFails: 5, window: time.Minute, block: time.Minute}
}

func clientIP(r *http.Request) string {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

// blockedFor returns how long ip is still blocked (0 = not blocked).
func (l *authLimiter) blockedFor(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.entries[ip]; e != nil {
		if d := e.blocked.Sub(l.now()); d > 0 {
			return d
		}
	}
	return 0
}

// fail records a wrong login.
func (l *authLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.entries) >= maxLimiterEntries {
		for k, e := range l.entries { // forget old entries so the map cannot grow without bound
			if now.Sub(e.first) > l.window && now.After(e.blocked) {
				delete(l.entries, k)
			}
		}
		if len(l.entries) >= maxLimiterEntries {
			l.entries = map[string]*failEntry{}
		}
	}
	e := l.entries[ip]
	if e == nil || now.Sub(e.first) > l.window {
		e = &failEntry{first: now}
		l.entries[ip] = e
	}
	e.fails++
	if e.fails >= l.maxFails {
		e.blocked = now.Add(l.block)
		e.fails = 0
		e.first = now
	}
}

// ok forgets the failures of ip after a correct login.
func (l *authLimiter) ok(ip string) {
	l.mu.Lock()
	delete(l.entries, ip)
	l.mu.Unlock()
}

func retryAfter(d time.Duration) string {
	return strconv.Itoa(int(d.Round(time.Second) / time.Second))
}

// limitListener allows at most n connections at the same time, so a flood of
// idle connections cannot exhaust the program's file descriptors.
type limitListener struct {
	net.Listener
	sem chan struct{}
}

func newLimitListener(l net.Listener, n int) net.Listener {
	return &limitListener{Listener: l, sem: make(chan struct{}, n)}
}

func (l *limitListener) Accept() (net.Conn, error) {
	l.sem <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitConn{Conn: c, release: func() { <-l.sem }}, nil
}

type limitConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
