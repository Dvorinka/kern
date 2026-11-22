package update

import (
	"sync"
	"time"
)

// Lease hands a self-replace to one runner at a time. The holder keeps the
// offer until the window ends, then another runner may take it. The lease
// is in-process: an API restart starts a fresh window.
type Lease struct {
	mu     sync.Mutex
	holder string
	until  time.Time
	Now    func() time.Time
}

func (l *Lease) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Allow grants id the offer. A different id is refused until the hold ends.
func (l *Lease) Allow(id string, hold time.Duration) bool {
	if id == "" {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if l.holder == id && now.Before(l.until) {
		return true
	}
	if now.Before(l.until) {
		return false
	}
	l.holder = id
	l.until = now.Add(hold)
	return true
}

// Release drops id's hold early. The fleet rollout used to idle out the
// full window even after the holder reached the target version, serializing
// N runners into N×hold. Called when a heartbeat reports the holder already
// runs the offered release; a non-holder release is a no-op.
func (l *Lease) Release(id string) {
	if id == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.holder == id {
		l.holder = ""
		l.until = time.Time{}
	}
}
