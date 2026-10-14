package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func dnsTestQuery(kind uint16) []byte {
	q := []byte{0x61, 0x7a, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0,
		7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 0, 0, 1}
	binary.BigEndian.PutUint16(q[len(q)-4:], kind)
	return q
}

func dnsTestAnswer(query []byte, code byte) []byte {
	_, end, err := dnsQuestionName(query, 12)
	if err != nil || end+4 > len(query) {
		return nil
	}
	response := append([]byte(nil), query[:end+4]...)
	response[2], response[3] = 0x81, 0x80|code
	for i := 6; i < 12; i++ {
		response[i] = 0
	}
	if code != 0 {
		return response
	}
	kind := binary.BigEndian.Uint16(query[end : end+2])
	ip := net.ParseIP("203.0.113.7").To4()
	if kind == 28 {
		ip = net.ParseIP("2001:db8::7").To16()
	}
	response[7] = 1
	response = append(response, 0xc0, 12, byte(kind>>8), byte(kind), 0, 1, 0, 0, 0, 60, 0, byte(len(ip)))
	return append(response, ip...)
}

func dnsTestTCPConn(answer func([]byte) []byte) net.Conn {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(5 * time.Second))
		length := make([]byte, 2)
		if _, err := io.ReadFull(server, length); err != nil {
			return
		}
		query := make([]byte, binary.BigEndian.Uint16(length))
		if _, err := io.ReadFull(server, query); err != nil {
			return
		}
		response := answer(query)
		binary.BigEndian.PutUint16(length, uint16(len(response)))
		_ = writeAll(server, append(length, response...))
	}()
	return client
}

func TestTunnelDNSProviderAndTransportFallback(t *testing.T) {
	for _, secondaryUDP := range []bool{false, true} {
		secondaryUDP := secondaryUDP
		t.Run(map[bool]string{false: "secondary TCP", true: "secondary UDP"}[secondaryUDP], func(t *testing.T) {
			var called []string
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			ips, err := resolveTunnelIPv4(ctx, "example.com", func(c context.Context, network, addr string) (net.Conn, error) {
				called = append(called, network+" "+addr)
				if addr != "8.8.8.8:53" || strings.HasPrefix(network, "udp") != secondaryUDP {
					return nil, errors.New("injected DNS provider/transport failure")
				}
				return dnsTestTCPConn(func(q []byte) []byte { return dnsTestAnswer(q, 0) }), nil
			})
			if err != nil || len(ips) != 1 || !ips[0].Equal(net.ParseIP("203.0.113.7")) {
				t.Fatalf("DNS fallback: ips=%v err=%v attempts=%v", ips, err, called)
			}
		})
	}
}

func TestTunnelDNSNegativeAndCancellation(t *testing.T) {
	_, err := resolveTunnelIPv4(context.Background(), "missing.invalid.", func(_ context.Context, _, addr string) (net.Conn, error) {
		if addr != "1.1.1.1:53" {
			t.Errorf("valid NXDOMAIN queried another provider: %s", addr)
		}
		return dnsTestTCPConn(func(q []byte) []byte { return dnsTestAnswer(q, 3) }), nil
	})
	var dnsErr *net.DNSError
	if !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
		t.Fatal("NXDOMAIN was not preserved", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = resolveTunnelIPv4(ctx, "example.com", func(c context.Context, _, _ string) (net.Conn, error) {
		return nil, c.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}

func TestDNSResponseValidation(t *testing.T) {
	query := dnsTestQuery(1)
	for _, kind := range []uint16{1, 28} {
		q := dnsTestQuery(kind)
		if err := validateDNSResponse(q, dnsTestAnswer(q, 0)); err != nil {
			t.Fatal("valid compressed A/AAAA answer rejected", err)
		}
	}
	if err := validateDNSResponse(query, dnsTestAnswer(query, 3)); err != nil {
		t.Fatal("valid NXDOMAIN rejected", err)
	}
	for _, code := range []byte{1, 2, 4, 5} {
		if err := validateDNSResponse(query, dnsTestAnswer(query, code)); err == nil {
			t.Fatalf("DNS error code %d was treated as success", code)
		}
	}
	for _, mutate := range []func([]byte){
		func(r []byte) { r[0]++ },
		func(r []byte) { r[2] |= 2 },
		func(r []byte) { r[13] = 'a' },
		func(r []byte) { r[12], r[13] = 0xc0, 12 }, // compression loop
		func(r []byte) { r[len(r)-5] = 255 },       // impossible RDLENGTH
	} {
		response := dnsTestAnswer(query, 0)
		mutate(response)
		if err := validateDNSResponse(query, response); err == nil {
			t.Fatalf("malformed DNS response accepted: %x", response)
		}
	}
}

// A real SOCKS UDP association used by Go's DNS parser, and by the opt-in live
// VPN check. The UDP packet source remains the DNS endpoint requested by the
// client even when the tunnel uses a different IPv4 upstream over TCP.
type socksDNSPacketConn struct {
	*net.UDPConn
	control net.Conn
	target  socksTarget
}

func (c *socksDNSPacketConn) Write(query []byte) (int, error) {
	packet, err := buildSOCKSUDPDatagram(c.target, query)
	if err != nil {
		return 0, err
	}
	_, err = c.UDPConn.Write(packet)
	if err != nil {
		return 0, err
	}
	return len(query), nil
}
func (c *socksDNSPacketConn) Read(buf []byte) (int, error) {
	packet := make([]byte, 65535)
	n, err := c.UDPConn.Read(packet)
	if err != nil {
		return 0, err
	}
	target, payload, err := parseSOCKSUDPDatagram(packet[:n])
	if err != nil {
		return 0, err
	}
	if target != c.target {
		return 0, errors.New("DNS response changed client's endpoint")
	}
	return copy(buf, payload), nil
}
func (c *socksDNSPacketConn) Close() error { _ = c.control.Close(); return c.UDPConn.Close() }

func dnsTestSOCKSRequest(address string, command byte, target socksTarget) (net.Conn, socksTarget, error) {
	c, err := net.DialTimeout("tcp4", address, 3*time.Second)
	if err != nil {
		return nil, socksTarget{}, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = c.Close()
		}
	}()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	if err := writeAll(c, []byte{5, 1, 0}); err != nil {
		return nil, socksTarget{}, err
	}
	head := make([]byte, 2)
	if _, err := io.ReadFull(c, head); err != nil {
		return nil, socksTarget{}, err
	}
	if !bytes.Equal(head, []byte{5, 0}) {
		return nil, socksTarget{}, errors.New("SOCKS auth failed")
	}
	encoded, err := encodeSOCKSTarget(target)
	if err != nil {
		return nil, socksTarget{}, err
	}
	if err := writeAll(c, append([]byte{5, command, 0}, encoded...)); err != nil {
		return nil, socksTarget{}, err
	}
	head = make([]byte, 4)
	if _, err := io.ReadFull(c, head); err != nil {
		return nil, socksTarget{}, err
	}
	if head[0] != 5 || head[1] != 0 {
		return nil, socksTarget{}, errors.New("SOCKS command failed")
	}
	bound, err := readSOCKSTarget(c, head[3])
	if err != nil {
		return nil, socksTarget{}, err
	}
	_ = c.SetDeadline(time.Time{})
	ok = true
	return c, bound, nil
}

func dnsTestPacketConn(address string, target socksTarget) (*socksDNSPacketConn, error) {
	control, bound, err := dnsTestSOCKSRequest(address, cmdUDP, socksTarget{host: "0.0.0.0"})
	if err != nil {
		return nil, err
	}
	remote, err := net.ResolveUDPAddr("udp4", bound.String())
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	conn, err := net.DialUDP("udp4", nil, remote)
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	return &socksDNSPacketConn{UDPConn: conn, control: control, target: target}, nil
}

func TestMobileDNSUpstreamErrorsFallBack(t *testing.T) {
	for _, code := range []byte{2, 5} {
		code := code
		for _, original := range []string{"192.168.1.1", "2001:4860:4860::8888"} {
			original := original
			t.Run(original+string(rune('0'+code)), func(t *testing.T) {
				listener, err := net.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
				go func() {
					for {
						c, err := listener.Accept()
						if err != nil {
							return
						}
						go serveSOCKSContext(c, func(ctx context.Context, network, addr string) (net.Conn, error) {
							if network != "tcp" {
								return nil, errors.New("mobile DNS must use TCP upstream")
							}
							return dnsTestTCPConn(func(q []byte) []byte {
								if addr == "1.1.1.1:53" {
									return dnsTestAnswer(q, code)
								}
								return dnsTestAnswer(q, 0)
							}), nil
						})
					}
				}()
				resolver := net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dnsTestPacketConn(listener.Addr().String(), socksTarget{host: original, port: 53})
				}}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				for _, network := range []string{"ip4", "ip6"} {
					ips, err := resolver.LookupIP(ctx, network, "example.com")
					if err != nil || len(ips) != 1 {
						t.Fatalf("client DNS parser %s: %v %v", network, ips, err)
					}
				}
			})
		}
	}
}

func TestDNSCancellationClosesActiveExchange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	remote := make(chan net.Conn, 1)
	done := make(chan error, 1)
	go func() {
		_, err := exchangeDNSOverTCP(ctx, func(context.Context, string, string) (net.Conn, error) {
			c, s := net.Pipe()
			remote <- s
			close(started)
			return c, nil
		}, socksTarget{host: "1.1.1.1", port: 53}, dnsTestQuery(1))
		done <- err
	}()
	<-started
	cancel()
	defer (<-remote).Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled DNS succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("active DNS did not cancel promptly")
	}
}
