package store

import (
	"context"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LeaderSession is a held Postgres session-level advisory lock on a dedicated
// pooled connection. While held, exactly one api replica leads scheduler
// ticks; if the connection dies the lock dies with it and another replica
// can take over on its next tick.
type LeaderSession struct {
	conn *pgxpool.Conn
	key  int64
	once sync.Once
}

// TryLeader attempts pg_try_advisory_lock on a dedicated connection. The
// boolean reports whether this caller now leads; the session must be
// Released (or the pool closed) to give the lock up.
func (s *Store) TryLeader(ctx context.Context, key int64) (*LeaderSession, bool, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, err
	}
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&ok); err != nil {
		conn.Release()
		return nil, false, err
	}
	if !ok {
		conn.Release()
		return nil, false, nil
	}
	return &LeaderSession{conn: conn, key: key}, true, nil
}

// Alive pings the lock-holding connection. A dead connection means the
// advisory lock is already gone — the caller must drop the session and
// re-contend rather than keep ticking as a false leader.
func (l *LeaderSession) Alive(ctx context.Context) bool {
	return l.conn.Conn().Ping(ctx) == nil
}

// Release unlocks and returns the connection to the pool. Idempotent.
func (l *LeaderSession) Release() {
	l.once.Do(func() {
		// Background context: the lock must be surrendered even when the
		// caller's request/tick context is already cancelled.
		_, _ = l.conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, l.key)
		l.conn.Release()
	})
}
