//go:build linux

package main

import (
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// Explicit opt-in only: creates its own temporary veth/netns and removes them.
// Verifies real namespace socket creation and restoration, including concurrent
// calls, without contacting volunteers or changing the host default route.
func TestLinuxNamespaceDialAndRestore(t *testing.T) {
	if os.Getenv("DPS_RUN_INTEGRATION") != "1" {
		t.Skip("requires isolated Linux runner and explicit opt-in")
	}
	if os.Geteuid() != 0 {
		t.Fatal("requires root")
	}
	ns := fmt.Sprintf("dps-test-%d", os.Getpid())
	host := fmt.Sprintf("dtv%d", os.Getpid())
	peer := fmt.Sprintf("dtp%d", os.Getpid())
	if len(host) > 15 {
		host = host[:15]
	}
	if len(peer) > 15 {
		peer = peer[:15]
	}
	command := func(args ...string) {
		t.Helper()
		if out, e := exec.Command("ip", args...).CombinedOutput(); e != nil {
			t.Fatalf("ip %v: %v %s", args, e, out)
		}
	}
	command("netns", "add", ns)
	defer exec.Command("ip", "netns", "del", ns).Run()
	defer exec.Command("ip", "link", "del", host).Run()
	command("link", "add", host, "type", "veth", "peer", "name", peer)
	command("link", "set", peer, "netns", ns)
	command("addr", "add", "198.18.255.1/30", "dev", host)
	command("link", "set", host, "up")
	command("netns", "exec", ns, "ip", "link", "set", peer, "name", "tun0")
	command("netns", "exec", ns, "ip", "addr", "add", "198.18.255.2/30", "dev", "tun0")
	command("netns", "exec", ns, "ip", "link", "set", "tun0", "up")
	command("netns", "exec", ns, "ip", "link", "set", "lo", "up")
	listener, e := net.Listen("tcp4", "198.18.255.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	go func() {
		for {
			c, e := listener.Accept()
			if e != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); io.Copy(c, c) }(c)
		}
	}()
	dial := dialerInNetns(ns)
	errors := make(chan error, 16)
	for i := 0; i < 16; i++ {
		go func() {
			c, e := dial("tcp", listener.Addr().String())
			if e == nil {
				defer c.Close()
				_, e = c.Write([]byte("test"))
				if e == nil {
					b := make([]byte, 4)
					_, e = io.ReadFull(c, b)
					if string(b) != "test" {
						e = fmt.Errorf("bad echo %q", b)
					}
				}
			}
			errors <- e
		}()
	}
	for i := 0; i < 16; i++ {
		if e := <-errors; e != nil {
			t.Fatal(e)
		}
	}
	// A newly locked main thread must remain in the host network namespace.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	origin, e := os.Open("/proc/thread-self/ns/net")
	if e != nil {
		t.Fatal(e)
	}
	defer origin.Close()
	target, e := os.Open("/var/run/netns/" + ns)
	if e != nil {
		t.Fatal(e)
	}
	defer target.Close()
	var a, b unix.Stat_t
	unix.Fstat(int(origin.Fd()), &a)
	unix.Fstat(int(target.Fd()), &b)
	if a.Ino == b.Ino {
		t.Fatal("namespace leaked into Go thread pool")
	}
	// Failure to enter a missing namespace is returned, never direct host dialing.
	if c, e := dialerInNetns(ns+"-missing")("tcp", listener.Addr().String()); e == nil {
		c.Close()
		t.Fatal("missing namespace fell back to host")
	}
	out, e := exec.Command("ip", "netns", "exec", ns, "ip", "route", "get", "198.18.255.1").Output()
	if e != nil || !strings.Contains(string(out), "tun0") {
		t.Fatal("test route invalid")
	}
}
func TestLinuxStoppedReconnectCannotDeleteReusedNamespace(t *testing.T) {
	if os.Getenv("DPS_RUN_INTEGRATION") != "1" {
		t.Skip("opt-in namespace integration")
	}
	tunnel := newTunnel(199, 31001, Node{HostName: "old"})
	if _, err := os.Stat("/var/run/netns/" + tunnel.nsName()); err == nil {
		t.Fatal("test namespace already exists; refusing to touch it")
	}
	if err := run("ip", "netns", "add", tunnel.nsName()); err != nil {
		t.Fatal(err)
	}
	defer runQuiet("ip", "netns", "del", tunnel.nsName())
	if err := run("ip", "-n", tunnel.nsName(), "link", "add", "fixture0", "type", "dummy"); err != nil {
		t.Fatal(err)
	}
	tunnel.stopped = true
	if tunnel.resetForReconnect() {
		t.Fatal("stopped tunnel allowed cleanup")
	}
	if err := run("ip", "-n", tunnel.nsName(), "link", "show", "fixture0"); err != nil {
		t.Fatal("old reconnect destroyed reused namespace", err)
	}
}
