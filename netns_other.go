//go:build !linux

package main

import (
	"context"
	"fmt"
	"net"
)

func dialerInNetns(_ string) func(network, addr string) (net.Conn, error) {
	return func(_, _ string) (net.Conn, error) {
		return nil, fmt.Errorf("dual-protocol-script-fix netns is only supported on Linux")
	}
}

func dialerInNetnsContext(nsName string) func(context.Context, string, string) (net.Conn, error) {
	dial := dialerInNetns(nsName)
	return func(ctx context.Context, n, a string) (net.Conn, error) { return dial(n, a) }
}
