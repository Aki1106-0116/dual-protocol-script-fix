//go:build linux

package main

import (
	"context"
	"net"
	"os"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// dialerInNetns creates sockets inside a tunnel's network namespace while the
// SOCKS5 listener itself remains on the host loopback interface.
func dialerInNetns(nsName string) func(network, addr string) (net.Conn, error) {
	dial := dialerInNetnsContext(nsName)
	return func(network, addr string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return dial(ctx, network, addr)
	}
}
func dialerInNetnsContext(nsName string) func(context.Context, string, string) (net.Conn, error) {
	var socketDial func(context.Context, string, string) (net.Conn, error)
	socketDial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		type result struct {
			conn net.Conn
			err  error
		}
		done := make(chan result, 1)
		go func() {
			runtime.LockOSThread()

			restored := true
			defer func() {
				if restored {
					runtime.UnlockOSThread()
				}
			}()
			origin, err := os.Open("/proc/thread-self/ns/net")
			if err != nil {
				done <- result{err: err}
				return
			}
			defer origin.Close()
			target, err := os.Open("/var/run/netns/" + nsName)
			if err != nil {
				done <- result{err: err}
				return
			}
			defer target.Close()
			if err := unix.Setns(int(target.Fd()), unix.CLONE_NEWNET); err != nil {
				done <- result{err: err}
				return
			}

			restored = false
			dialer := net.Dialer{Timeout: 10 * time.Second, Control: func(n, a string, raw syscall.RawConn) error {
				var e error
				err := raw.Control(func(fd uintptr) { e = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, "tun0") })
				if err != nil {
					return err
				}
				return e
			}}
			conn, dialErr := dialer.DialContext(ctx, forceIPv4Network(network), addr)
			if err := unix.Setns(int(origin.Fd()), unix.CLONE_NEWNET); err != nil {
				if conn != nil {
					_ = conn.Close()
				}
				done <- result{err: err}
				return
			}
			restored = true
			done <- result{conn: conn, err: dialErr}
		}()
		r := <-done
		return r.conn, r.err
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if net.ParseIP(host) == nil {
			ips, err := resolveTunnelIPv4(ctx, host, socketDial)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, &net.DNSError{Name: host, Err: "没有 IPv4 地址"}
			}
			host = ips[0].String()
		}
		return socketDial(ctx, network, net.JoinHostPort(host, port))
	}
}

func forceIPv4Network(network string) string {
	switch network {
	case "tcp":
		return "tcp4"
	case "udp":
		return "udp4"
	default:
		return network
	}
}
