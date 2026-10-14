# DNS 专项排错与验证

节点握手成功或测得真实连接延迟，不能证明浏览器的域名解析正常。本轮检查了配置生成、VLESS/HY2 路由、隧道域名解析、SOCKS5 UDP DNS 转发和出口切换。

## 找到并修复的缺陷

1. 隧道的 Go 域名解析器固定使用 `1.1.1.1:53`。虽然已有 UDP → TCP 回退，但该服务两个传输都不可达时，不会使用 namespace 配置中的备用 DNS。现优先尝试主服务 UDP，然后依次尝试 `1.1.1.1`、`8.8.8.8`、`9.9.9.9` 的 TCP，最后保留备用服务 UDP。每次尝试受独立预算及调用者总截止时间约束。
2. 手机 UDP DNS 转 TCP 后，只检查响应 ID 和 QR，收到 SERVFAIL/REFUSED 也当作成功返回，客户端仍然解析失败。现验证 ID、opcode、问题名称/类型/类别、压缩名称和记录长度；错误、截断或不匹配的响应会尝试备用服务。正常 NXDOMAIN、正常无该类型记录的结果，以及 A/AAAA、DNSSEC/HTTPS 等未知记录类型保持原语义，不通过修改答案隐藏问题。
3. 原 DNS TCP 的 4 秒响应超时在拨号完成后才开始，拨号自身还可能等待 10 秒。现连接和读写共享 4 秒截止时间；生产调用使用可取消的 namespace 拨号，客户端关闭 UDP 控制连接会取消 DNS 查询及备用尝试，并关闭进行中的 DNS 连接。

所有备用解析和手机 DNS 转发都经所选 VPN namespace，并绑定 `tun0`。不会用宿主机直连绕过失效出口。原有手机私网/IPv6 DNS 地址映射公共 IPv4 服务的行为保留，响应 SOCKS5 地址仍是客户端请求的原地址。普通非 DNS UDP 和 TCP 转发保留原路径。

## 配置链路检查

- Xray 家宽路由仍覆盖对应 VLESS 入站的 TCP/UDP；不会插入绕过家宽的 DNS 直连规则。
- HY2 家宽 SOCKS5 出站没有开启 `disableUDP`；手机 DNS 可达同一 UDP 转发器。
- HY2 入站 QUIC、端口跳跃和客户端 SOCKS5 配置保持原实现。REALITY 本地自检中的 `udp: false` 只用于临时 TCP 自检，不是生产入站配置。
- VPN Gate 出口为 IPv4。DNS 请求本身可以查询并返回 AAAA，IPv6 字面量的业务连接仍保持原有不支持行为；本轮没有过滤 AAAA 或改写浏览器 DNS 答案。

## 已通过的验证

Windows `go test ./...` 和 `go vet ./...` 通过。Ubuntu 24.04 / Go 1.22.2 / GCC 13 的 `DPS_RUN_INTEGRATION=1 go test -race ./...` 通过（17.776 秒），含 namespace 集成回归。Linux amd64、arm64 构建通过。

新增回归用真实 DNS wire 消息与 Go 的 DNS 解析器检查：备用服务 TCP/UDP、SERVFAIL/REFUSED 后重试、A/AAAA、NXDOMAIN 保留、问题不匹配、压缩指针循环、坏记录长度，以及取消查询后关闭活动连接。原 TCP/UDP、路由、配置和服务事务的 Go 回归同时通过。

隔离 Linux 虚拟机连接官方列表中的真实 OpenVPN 出口，进行以下故障注入：

1. 在出口 namespace 封锁所有 UDP/53。客户端分别以私网 DNS 地址 `192.168.1.1` 和 IPv6 DNS 地址 `2001:4860:4860::8888` 发出 SOCKS5 UDP DNS 请求；A/AAAA 均通过 Go DNS 解析器获得有效结果。再经 SOCKS5 域名 CONNECT 访问 `https://example.com/`，验证 TLS、HTTP 200 和网页正文。
2. 保持 UDP/53 封锁，再封锁 `1.1.1.1:53` 的 TCP。重复上述四组解析及完整 HTTPS 网页访问，全部通过，确认备用 DNS 实际生效。
3. 移除测试规则并终止原 OpenVPN 进程，自动切换到另一可用出口，槽位和本地 SOCKS5 端口保持一致。

```text
official nodes=97
LIVE_VPN_OK country=JP endpoint=219.62.85.155 exit=219.62.85.155
UDP_DNS_BLOCKED_TCP_FALLBACK_OK
LIVE_CLIENT_DNS_OK endpoint=192.168.1.1 family=ip4 answers=2
LIVE_CLIENT_DNS_OK endpoint=192.168.1.1 family=ip6 answers=2
LIVE_CLIENT_DNS_OK endpoint=2001:4860:4860::8888 family=ip4 answers=2
LIVE_CLIENT_DNS_OK endpoint=2001:4860:4860::8888 family=ip6 answers=2
LIVE_CLIENT_HTTPS_PAGE_OK
PRIMARY_DNS_AND_UDP_BLOCKED_CLIENT_DNS_HTTPS_OK
LIVE_FAILOVER_OK endpoint=221.44.205.161 exit=221.44.205.161 slot=1 port=48399
PASS (110.32s)
```

总耗时包含列表下载、候选连接、两组故障注入、网页验证和出口切换，不是 DNS 延迟或恢复时间承诺。正常解析不并发广播给所有备用服务，没有增加常驻进程、缓存轮询或健康检查频率；只在失败时增加备用请求。DNS 工作仍受每个 UDP 会话最多 64 个并发请求限制。

完整真实出口测试仍为默认跳过的 opt-in 测试，使用方法见 [VALIDATION.md](VALIDATION.md)。没有旧故障客户端/服务器日志，不能确认历史故障根因；上述真实测试覆盖面板维护的 SOCKS5 → DNS/HTTPS → OpenVPN 链路，未冒充完整 Xray/HY2 客户端和公网 ACME 安装验收。

响应格式、RCODE 和 TCP 长度前缀依据 [RFC 1035](https://www.rfc-editor.org/rfc/rfc1035.html)，DNS TCP 支持依据 [RFC 7766](https://www.rfc-editor.org/rfc/rfc7766.html)。
