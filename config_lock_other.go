//go:build !linux

package main

import (
	"context"
	"os/exec"
)

var portableConfigLock = make(chan struct{}, 1)

func acquireConfigLock(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case portableConfigLock <- struct{}{}:
		return func() { <-portableConfigLock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func configureCancellation(cmd *exec.Cmd) {}
