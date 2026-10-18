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

// loginLimiter counts login attempts per client IP within a sliding window. It is
// a per-process throttle: it stops password guessing over the network, not a
// distributed attack.
//
// An attempt is recorded when it is admitted, not when it turns out to be wrong.
// Reserving first is what makes the limit hold under concurrency: if only
// failures were counted, N simultaneous guesses would all read the same empty
// history, all pass the check, and all reach the password comparison.
type loginLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	attempts map[string][]time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{max: max, window: window, attempts: map[string][]time.Time{}}
}

// reserve admits one attempt and records it. It reports false when ip has used up
// its quota, along with how long until the oldest attempt leaves the window. The
// lock covers only the counter, never the request body read or the password
// comparison, so a slow client cannot stall other logins.
func (l *loginLimiter) reserve(ip string, at time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.pruneLocked(ip, at)
	if len(recent) >= l.max {
		return l.window - at.Sub(recent[0]), false
	}
	l.attempts[ip] = append(recent, at)
	return 0, true
}

// succeed clears the record so a legitimate login does not leave the IP one
// attempt from the limit, and does not let it be counted against the next one.
func (l *loginLimiter) succeed(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}

func (l *loginLimiter) pruneLocked(ip string, at time.Time) []time.Time {
	cutoff := at.Add(-l.window)
	kept := l.attempts[ip][:0]
	for _, t := range l.attempts[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.attempts, ip)
		return nil
	}
	l.attempts[ip] = kept
	return kept
}
