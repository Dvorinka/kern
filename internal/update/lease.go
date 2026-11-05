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
