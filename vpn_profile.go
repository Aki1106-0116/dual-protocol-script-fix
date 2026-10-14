package main

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

type vpnProfile struct {
	Config            string
	Host              string
	Port              int
	Proto             string
	LegacyCertificate bool
}

// Never execute the downloaded OpenVPN file as a root configuration. Keep
// only inline trust material and validated connection parameters. All files,
// plugins, hooks, management listeners and server-mode options are excluded.
func normalizeVPNProfile(node Node) (vpnProfile, error) {
	var p vpnProfile
	if len(node.Config) > 256<<10 {
		return p, errors.New("OpenVPN 配置过大")
	}
	ip := net.ParseIP(node.IP)
	if ip == nil || ip.To4() == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return p, errors.New("VPN Gate IP 不是公网 IPv4")
	}
	blocks := map[string]string{}
	var block string
	var content []string
	proto := "tcp-client"
	port := 0
	cipher := "AES-128-CBC"
	auth := "SHA1"
	for _, raw := range strings.Split(node.Config, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if block != "" {
			if line == "</"+block+">" {
				if blocks[block] != "" {
					return p, errors.New("重复的内联证书块")
				}
				blocks[block] = strings.Join(content, "\n") + "\n"
				block = ""
				content = nil
			} else {
				content = append(content, raw)
			}
			continue
		}
		if strings.HasPrefix(line, "<") {
			name := strings.Trim(line, "<>")
			if name != "ca" && name != "cert" && name != "key" {
				return p, fmt.Errorf("拒绝内联块 %q", name)
			}
			block = name
			continue
		}
		f := strings.Fields(line)
		key := strings.TrimPrefix(f[0], "--")
		switch key {
		case "remote":
			if len(f) < 3 || len(f) > 4 {
				return p, errors.New("remote 参数无效")
			}
			n, e := strconv.Atoi(f[2])
			if e != nil || n < 1 || n > 65535 {
				return p, errors.New("remote 端口无效")
			}
			if port != 0 && port != n {
				return p, errors.New("不支持混合端口 profile")
			}
			port = n
		case "proto":
			if len(f) != 2 {
				return p, errors.New("proto 无效")
			}
			switch f[1] {
			case "tcp", "tcp-client", "tcp4", "tcp4-client":
				proto = "tcp-client"
			case "udp", "udp4":
				proto = "udp"
			default:
				return p, errors.New("不支持 OpenVPN 协议")
			}
		case "cipher":
			if len(f) != 2 {
				return p, errors.New("cipher 无效")
			}
			cipher = f[1]
			if cipher != "AES-128-CBC" && cipher != "AES-256-CBC" && cipher != "AES-128-GCM" && cipher != "AES-256-GCM" {
				return p, errors.New("拒绝不支持的加密算法")
			}
		case "auth":
			if len(f) != 2 {
				return p, errors.New("auth 无效")
			}
			auth = f[1]
			if auth != "SHA1" && auth != "SHA256" && auth != "SHA512" {
				return p, errors.New("auth 摘要无效")
			}
		case "dev", "client", "nobind", "persist-key", "persist-tun", "resolv-retry", "verb", "remote-cert-tls", "data-ciphers", "data-ciphers-fallback", "auth-user-pass", "redirect-gateway", "route", "route-metric", "tun-mtu", "mssfix", "remote-random", "auth-nocache", "comp-lzo", "compress", "keepalive", "ping", "ping-restart", "sndbuf", "rcvbuf", "reneg-sec", "explicit-exit-notify", "tls-version-min":
			// Replaced below with owned safe defaults; compression stays disabled.
		default:
			return p, fmt.Errorf("拒绝第三方 OpenVPN 指令 %q", key)
		}
	}
	if block != "" || port == 0 {
		return p, errors.New("OpenVPN 配置不完整")
	}
	for _, key := range []string{"ca", "cert", "key"} {
		if blocks[key] == "" {
			return p, fmt.Errorf("缺少内联 %s", key)
		}
	}
	for _, name := range []string{"ca", "cert"} {
		rest := []byte(blocks[name])
		count := 0
		for len(rest) > 0 {
			b, r := pem.Decode(rest)
			if b == nil {
				return p, errors.New("证书 PEM 无效")
			}
			rest = r
			c, e := x509.ParseCertificate(b.Bytes)
			if e != nil {
				return p, e
			}
			count++
			if name == "cert" && (c.SignatureAlgorithm == x509.SHA1WithRSA || c.SignatureAlgorithm == x509.ECDSAWithSHA1) {
				p.LegacyCertificate = true
			}
		}
		if count == 0 {
			return p, errors.New("证书为空")
		}
	}
	p.Host = ip.String()
	p.Port = port
	p.Proto = proto
	p.Config = fmt.Sprintf("client\ndev tun0\nproto %s\nremote %s %d\nnobind\npersist-key\npersist-tun\nremote-cert-tls server\ncipher %s\ndata-ciphers AES-256-GCM:AES-128-GCM:%s\ndata-ciphers-fallback %s\nauth %s\nredirect-gateway def1\nscript-security 1\nauth-nocache\nallow-compression no\npull-filter ignore route\npull-filter ignore redirect-gateway\npull-filter ignore dhcp-option\npull-filter ignore block-outside-dns\n", proto, p.Host, port, cipher, cipher, cipher, auth)
	// Keep route-gateway: OpenVPN needs it to install our owned redirect rule.
	p.Config = strings.ReplaceAll(p.Config, "pull-filter ignore route\n", "pull-filter ignore \"route \"\n")
	// Keep the normal certificate policy. The connector may retry a specific
	// local SHA-1 client-certificate loading error, without disabling CA/date
	// verification or weakening TLS globally.
	p.Config += "tls-version-min 1.2\n"
	for _, name := range []string{"ca", "cert", "key"} {
		p.Config += "<" + name + ">\n" + blocks[name] + "</" + name + ">\n"
	}
	return p, nil
}
