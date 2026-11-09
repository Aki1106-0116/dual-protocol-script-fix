package main

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"time"
)

// Every DNS socket uses the injected tunnel dialer. Never fall back to the host
// resolver: that would leak queries and bypass the selected exit's policy.
func resolveTunnelIPv4(ctx context.Context, host string, dial func(context.Context, string, string) (net.Conn, error)) ([]net.IP, error) {
	lookup := func(target socksTarget, tcp bool, timeout time.Duration) ([]net.IP, error) {
		resolver := net.Resolver{PreferGo: true, Dial: func(c context.Context, network, _ string) (net.Conn, error) {
			if tcp {
				network = "tcp"
			}
			return dial(c, network, target.String())
		}}
		c, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return resolver.LookupIP(c, "ip4", host)
	}
	var last error
	try := func(target socksTarget, tcp bool, timeout time.Duration) ([]net.IP, bool) {
		ips, err := lookup(target, tcp, timeout)
		last = err
		var dnsErr *net.DNSError
		// A valid negative answer must remain negative; another provider must
		// not turn NXDOMAIN or an IPv6-only name into a different result.
		return ips, err == nil || errors.As(err, &dnsErr) && dnsErr.IsNotFound
	}
	if ips, done := try(dnsFallbackTargets[0], false, 2*time.Second); done {
		return ips, last
	}
	// TCP is preferred after a UDP failure because many VPN Gate exits block
	// UDP/53. Each provider has its own budget inside the caller's deadline.
	for _, target := range dnsFallbackTargets {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if ips, done := try(target, true, 2*time.Second); done {
			return ips, last
		}
	}
	// Other exits block TCP/53 or only one provider. Retain UDP on backups.
	for _, target := range dnsFallbackTargets[1:] {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if ips, done := try(target, false, time.Second); done {
			return ips, last
		}
	}
	return nil, last
}

func validateDNSResponse(query, response []byte) error {
	if len(query) < 12 || len(response) < 12 || query[2]&0x80 != 0 ||
		response[0] != query[0] || response[1] != query[1] || response[2]&0x80 == 0 ||
		response[2]&0x78 != query[2]&0x78 {
		return errors.New("DNS 响应与请求不匹配")
	}
	if response[2]&2 != 0 {
		return errors.New("DNS TCP 响应被截断")
	}
	if code := response[3] & 15; code != 0 && code != 3 {
		return errors.New("DNS 服务返回错误，尝试备用服务")
	}
	count := int(binary.BigEndian.Uint16(query[4:6]))
	if count != int(binary.BigEndian.Uint16(response[4:6])) {
		return errors.New("DNS 响应问题数量不匹配")
	}
	q, r := 12, 12
	for i := 0; i < count; i++ {
		qn, nextQ, errQ := dnsQuestionName(query, q)
		rn, nextR, errR := dnsQuestionName(response, r)
		if errQ != nil || errR != nil || qn != rn || nextQ+4 > len(query) || nextR+4 > len(response) ||
			string(query[nextQ:nextQ+4]) != string(response[nextR:nextR+4]) {
			return errors.New("DNS 响应问题不匹配")
		}
		q, r = nextQ+4, nextR+4
	}
	// Unknown RR types are preserved. Only check wire lengths, so malformed
	// upstream responses can fail over without interpreting DNSSEC/HTTPS data.
	records := int(binary.BigEndian.Uint16(response[6:8])) + int(binary.BigEndian.Uint16(response[8:10])) + int(binary.BigEndian.Uint16(response[10:12]))
	for i := 0; i < records; i++ {
		_, end, err := dnsQuestionName(response, r)
		if err != nil || end+10 > len(response) {
			return errors.New("DNS 响应记录不完整")
		}
		r = end + 10 + int(binary.BigEndian.Uint16(response[end+8:end+10]))
		if r > len(response) {
			return errors.New("DNS 响应数据不完整")
		}
	}
	if r != len(response) {
		return errors.New("DNS 响应包含无效尾部数据")
	}
	return nil
}

// Return a canonical length-prefixed name (ASCII case-insensitive), following
// compression pointers with bounded work and preserving label boundaries.
func dnsQuestionName(message []byte, offset int) (string, int, error) {
	name := make([]byte, 0, 64)
	next := -1
	for steps := 0; steps < 128 && offset < len(message); steps++ {
		size := int(message[offset])
		offset++
		if size&0xc0 == 0xc0 {
			if offset >= len(message) {
				break
			}
			if next < 0 {
				next = offset + 1
			}
			offset = (size&0x3f)<<8 | int(message[offset])
			continue
		}
		if size > 63 || offset+size > len(message) || len(name)+size+1 > 255 {
			break
		}
		name = append(name, byte(size))
		if size == 0 {
			if next < 0 {
				next = offset
			}
			return string(name), next, nil
		}
		for _, b := range message[offset : offset+size] {
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			name = append(name, b)
		}
		offset += size
	}
	return "", 0, errors.New("DNS 名称编码无效")
}
