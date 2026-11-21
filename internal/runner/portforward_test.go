package runner

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// RunPortForward must splice bytes both ways — the whole point is
// localhost:<hostPort> answering with the service's protocol bytes.
func TestRunPortForwardSplices(t *testing.T) {
	// Upstream echo server.
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		conn, err := up.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		spliceTCP(conn, up.Addr().String())
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("kern")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "kern" {
		t.Fatalf("echo mismatch: %q", buf)
	}
}

// Half-close regression: when the client calls CloseWrite after sending its
// request, spliceTCP must wait for the upstream response instead of closing
// both sockets on the first EOF.
func TestSpliceTCPHalfClose(t *testing.T) {
	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	go func() {
		conn, err := up.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		req, _ := io.ReadAll(conn)
		_, _ = conn.Write(append([]byte("ok:"), req...))
	}()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		spliceTCP(conn, up.Addr().String())
	}()

	conn, err := net.DialTCP("tcp", nil, ln.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	if string(resp) != "ok:ping" {
		t.Fatalf("half-close response = %q, want ok:ping", resp)
	}
}

func TestRunPortForwardShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunPortForward(ctx, "127.0.0.1:0", "127.0.0.1:1")
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunPortForward shutdown err = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunPortForward did not exit on context cancel")
	}
}
