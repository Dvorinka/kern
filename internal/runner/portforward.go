package runner

// The port-forwarder honours services.<name>.ports host:container remaps on
// bridge jobs. The service shares the job's network namespace and binds its
// own port; this sibling — the same kern-egress:local image that carries
// the runner binary, invoked as `port-forward` — listens on the declared
// host port inside that netns and splices bytes to the service port, so
// `localhost:<hostPort>` answers the way GitHub-hosted runners promise.
// TCP only: protocol remaps other than /tcp warn and are skipped upstream.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

// RunPortForward is the `kern-runner port-forward` entrypoint: a minimal
// TCP splice that accepts on listen and proxies each connection to to.
// Both are host:port; listen's host may be empty ("*:port" shape ":5433").
func RunPortForward(ctx context.Context, listen, to string) error {
	if listen == "" || to == "" {
		return fmt.Errorf("port-forward requires --listen and --to")
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listen, err)
	}
	defer ln.Close()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go spliceTCP(conn, to)
	}
}

func spliceTCP(conn net.Conn, to string) {
	defer conn.Close()
	up, err := net.DialTimeout("tcp", to, 10*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "port-forward dial %s: %v\n", to, err)
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if tc, ok := dst.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}
	go cp(up, conn)
	go cp(conn, up)
	// Wait for both directions so a half-closed client (CloseWrite) still
	// receives the full upstream response before both sockets close.
	<-done
	<-done
}
