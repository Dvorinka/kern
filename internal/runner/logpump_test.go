package runner

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// logSink counts appendLog POSTs and captures the merged byte stream.
type logSink struct {
	t    *testing.T
	n    atomic.Int32
	data chan []byte
}

func (l *logSink) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.n.Add(1)
		body, _ := io.ReadAll(r.Body)
		l.data <- body
		w.WriteHeader(http.StatusOK)
	})
}

// The pump must batch: small writes flushed by interval, large writes flushed
// by size — either way the control plane sees few, big POSTs and the bytes
// arrive complete and ordered.
func TestLogPumpBatches(t *testing.T) {
	sink := &logSink{t: t, data: make(chan []byte, 16)}
	srv := httptest.NewServer(sink.handler())
	defer srv.Close()

	c := newClient(srv.URL, "")
	p := newLogPump(c, "job-1", 1, nil, nil)

	p.write([]byte("line one\n"))
	p.write([]byte("line two\n"))
	p.write([]byte("line three\n"))
	if got := sink.n.Load(); got != 0 {
		t.Fatalf("expected no POST before flush interval, got %d", got)
	}
	p.flush()
	p.close()

	if got := sink.n.Load(); got != 1 {
		t.Fatalf("expected exactly one POST for three small writes, got %d", got)
	}
	select {
	case body := <-sink.data:
		if string(body) != "line one\nline two\nline three\n" {
			t.Fatalf("log body = %q", body)
		}
	default:
		t.Fatal("no log body captured")
	}
}

// A write above the size threshold must flush immediately rather than wait for
// the interval — that is what bounds memory on loud steps.
func TestLogPumpSizeFlush(t *testing.T) {
	sink := &logSink{t: t, data: make(chan []byte, 16)}
	srv := httptest.NewServer(sink.handler())
	defer srv.Close()

	c := newClient(srv.URL, "")
	p := newLogPump(c, "job-1", 1, nil, nil)
	defer p.close()

	big := make([]byte, logFlushBytes+1)
	for i := range big {
		big[i] = 'x'
	}
	p.write(big)
	// The flush is issued synchronously from write(); allow the HTTP round trip.
	deadline := time.Now().Add(3 * time.Second)
	for sink.n.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if sink.n.Load() == 0 {
		t.Fatal("oversized write did not trigger a size flush")
	}
	select {
	case body := <-sink.data:
		if len(body) != len(big) {
			t.Fatalf("flushed %d bytes, want %d", len(body), len(big))
		}
	default:
		t.Fatal("no log body captured")
	}
}

func TestLogPumpChunkingAndRetry(t *testing.T) {
	var calls atomic.Int32
	var received [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		received = append(received, body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newClient(srv.URL, "")
	p := newLogPump(c, "job-1", 1, nil, nil)
	big := bytes.Repeat([]byte("a"), logMaxChunkBytes+100)
	p.write(big)
	p.close()

	if len(received) < 2 {
		t.Fatalf("expected >= 2 chunks for >512KiB write, got %d", len(received))
	}
	var total int
	for _, ch := range received {
		if len(ch) > logMaxChunkBytes {
			t.Fatalf("chunk exceeded %d bytes: %d", logMaxChunkBytes, len(ch))
		}
		total += len(ch)
	}
	if total != len(big) {
		t.Fatalf("total received = %d, want %d (retry dropped data)", total, len(big))
	}
}

func TestStreamIntoLargeLineNoNewline(t *testing.T) {
	sink := &logSink{t: t, data: make(chan []byte, 16)}
	srv := httptest.NewServer(sink.handler())
	defer srv.Close()

	c := newClient(srv.URL, "")
	p := newLogPump(c, "job-1", 1, nil, nil)
	payload := strings.Repeat("z", 200<<10)
	streamInto(strings.NewReader(payload), p)
	p.close()

	var got bytes.Buffer
	for len(sink.data) > 0 {
		got.Write(<-sink.data)
	}
	if got.Len() != len(payload) {
		t.Fatalf("streamInto got %d bytes, want %d", got.Len(), len(payload))
	}
}

func TestTrimServiceLogCapUTF8Safe(t *testing.T) {
	line := "service:db | žluťoučký kůň úpěl ďábelské ódy\n"
	buf := []byte(strings.Repeat(line, 20))
	trimmed := trimServiceLogCap(buf, 200)
	if !utf8.Valid(trimmed) {
		t.Fatalf("trimmed output is not valid UTF-8")
	}
	if !strings.HasPrefix(string(trimmed), "service:log | ... truncated ...\n") {
		t.Fatalf("missing truncation marker: %q", string(trimmed[:40]))
	}
	if len(trimmed) > 200 {
		t.Fatalf("trimmed length %d > 200", len(trimmed))
	}
}
