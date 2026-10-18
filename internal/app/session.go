package app

import (
	"sync"
	"time"
)

const sessionCookie = "session"

// sessionTable holds live sessions in memory, so a restart invalidates every
// session by construction. It is safe for concurrent use.
type sessionTable struct {
	mu       sync.Mutex
	ttl      time.Duration
	sessions map[string]time.Time // token -> expiry
}

func newSessionTable(ttl time.Duration) *sessionTable {
	return &sessionTable{ttl: ttl, sessions: map[string]time.Time{}}
}

func (t *sessionTable) create() (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(time.Now())
	t.sessions[token] = time.Now().Add(t.ttl)
	return token, nil
}

func (t *sessionTable) lookup(token string) (time.Time, bool) {
	if token == "" {
		return time.Time{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	expiry, ok := t.sessions[token]
	if !ok || time.Now().After(expiry) {
		return time.Time{}, false
	}
	return expiry, true
}

func (t *sessionTable) drop(token string) {
	if token == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.sessions, token)
}

// pruneLocked drops expired sessions. Called on create so the table cannot grow
// without bound on a long-running process.
func (t *sessionTable) pruneLocked(at time.Time) {
	for token, expiry := range t.sessions {
		if at.After(expiry) {
			delete(t.sessions, token)
		}
	}
}

// loginLimiter counts failed logins per client IP within a sliding window. It is
// a per-process throttle: it stops password guessing over the network, not a
// distributed attack.
type loginLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	failures map[string][]time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{max: max, window: window, failures: map[string][]time.Time{}}
}

// blocked reports whether ip has exhausted its attempts, and how long until the
// oldest recorded failure leaves the window.
func (l *loginLimiter) blocked(ip string, at time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.pruneLocked(ip, at)
	if len(recent) < l.max {
		return 0, false
	}
	return l.window - at.Sub(recent[0]), true
}

func (l *loginLimiter) fail(ip string, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[ip] = append(l.pruneLocked(ip, at), at)
}

// succeed clears the record so a legitimate login does not leave the IP one
// attempt from the limit.
func (l *loginLimiter) succeed(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, ip)
}

func (l *loginLimiter) pruneLocked(ip string, at time.Time) []time.Time {
	cutoff := at.Add(-l.window)
	kept := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, ip)
	} else {
		l.failures[ip] = kept
	}
	return kept
}
