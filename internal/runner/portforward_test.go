package runner

import (
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
