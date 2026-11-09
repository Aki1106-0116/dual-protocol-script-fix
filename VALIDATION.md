# 验证记录

验证日期：2026-10-14。原项目保持原样；所有修改位于独立的 `dual-protocol-script-fix` 源码目录。本记录说明已取得的证据，不把没有测试的部署条件描述为已验证。

## 环境

- 本机 Windows：Go 1.27.1、Git Bash、Python 3、真实 Chrome 的无头浏览器模式。
- 隔离 Linux 虚拟机：Ubuntu 24.04、内核 6.8.0-146、systemd、OpenVPN 2.6、iptables、network namespace；1536 MiB 内存、2 个模拟 CPU。虚拟机只用于测试，不在 Windows 安装代理或系统服务。
- Linux 竞态检测使用 Go 1.22.2 与 GCC 13；普通 Linux 测试使用从本机交叉编译的测试程序。

## 已通过的回归

| 检查 | 结果与范围 |
|---|---|
| `go test ./...` | Windows、Linux 通过；覆盖配置、路由、HTTP、TLS、SOCKS5 TCP/UDP/DNS、生命周期及恢复逻辑 |
| `go vet ./...` | 通过 |
| `go test -race ./...` | Ubuntu / Go 1.22.2 / GCC 13 通过，未检测到数据竞争 |
| `npm ci` | 从交付的锁定文件安装成功；运行面板不需要这些开发依赖 |
| Python 存储与迁移测试 | 13 项通过；含 EIO、失败原子替换、损坏校验和、无效 JSON/YAML、旧混淆迁移、非法 shell 赋值、缺失状态字段、备份失败、大文件及读回失败 |
| Bash 语法检查 | 所有安装、启动、模块、同步、包装和测试脚本通过 |
| Bash 事务回归 | Windows Git Bash 与 Linux 通过；覆盖 REALITY、XHTTP、端口跳跃、Gecko、凭据中的特殊字符、客户端导出、服务失败后的完整回滚 |
| 真实浏览器 | 登录、节点保存、组合切换、面板设置、服务重启、分享、创建出口通过；桌面及 390 px 手机布局无横向溢出或 JavaScript 错误 |
| Linux netns 集成 | 16 个并发 socket 在指定 namespace 建立；恢复正确线程的 namespace；通过 |
| Linux amd64 / arm64 构建 | 两种架构均编译通过；arm64 未在真实设备执行 |
| systemd HTTPS 面板 | 真实启动器、TLS、登录、状态与静态资源通过；代理服务使用测试替身，未冒充真实协议转发 |
| 配置权限 | 非服务用户不能读取私密配置；指定服务用户通过 ACL 可以读取；其他用户权限为 `---` |

Go 恢复测试还验证了：损坏的面板环境不会污染已验证副本，恢复后实际文件与原健康内容一致；停止的出口不会被迟到连接结果重新标为可用；跨站修改被拒绝；被删除功能的 API 字段被拒绝。

## 真实 VPN Gate 测试

直接获取 [官方 API](https://www.vpngate.net/api/iphone/) 的一批 100 个配置，全部可解析和规范化。Linux 测试时官方返回 99 个，数量随志愿服务器上下线变化。

隔离虚拟机进行了真实 OpenVPN 连接和故障注入。最终一轮记录：

```text
LIVE_VPN_OK country=JP endpoint=211.10.52.110 exit=211.10.52.110
UDP_DNS_BLOCKED_TCP_FALLBACK_OK
LIVE_FAILOVER_OK endpoint=133.32.165.70 exit=133.32.165.226 slot=1 port=42842
PASS (78.58s，含列表下载、初次连接、DNS 故障注入和切换)
```

测试确认：HTTPS 请求实际经过隧道；主动阻断 namespace 的 UDP DNS 后 TCP DNS 回退仍有效；终止原 OpenVPN 进程后后台自动选到新节点；槽位和本地 SOCKS5 端口保持一致。另两轮真实连接/切换分别通过，耗时 15.24 秒和 25.38 秒，不能将总测试耗时当作所有网络的恢复时间保证。

默认证书策略的独立对照测试到达服务器 `AUTH_FAILED`，没有触发本地 SHA-1 加载错误。故默认使用正常证书策略，仅在确切的本地过弱摘要加载错误时重试兼容设置。无法用这个结果断言用户历史故障也来自认证；没有原服务器日志。

真实连接测试默认不随 `go test` 运行。要主动使用志愿出口，在独立 Linux 测试环境运行：

```bash
go test -c -o /tmp/dps-integration.test .
sudo env DPS_RUN_LIVE_VPN=1 /tmp/dps-integration.test -test.run '^TestLinuxLiveVPNGate$' -test.v -test.timeout 10m
```

该测试只应在独立环境运行：它会准备转发规则、建立自己的出口 namespace，结束后清理出口。官方节点可能认证失败、离线或被所在网络过滤，失败日志用于区分原因。

## 资源与性能

节点保存专项：新增真实 Go→Bash flock/状态/导出/Caddy 校验集成，两种组合均通过；验证未改域名复用证书、改变单个域名只进入该域名签发流程，以及禁止缓存写入和读回失败后报成功。完整 Linux race 与 namespace/后端集成通过（102.504 秒）；范围与升级命令见 [NODE_SAVE.md](NODE_SAVE.md)。

DNS 专项补充：真实出口中同时封锁 UDP/53 和主 DNS 的 TCP/53 后，手机 SOCKS5 UDP 的四组 A/AAAA 解析及完整 HTTPS 网页访问均通过，随后自动切换也通过；本轮总计 110.32 秒。新增 DNS 回归及 namespace 集成的 Linux race 检查通过（17.776 秒）。缺陷、测试输出和范围见 [DNS.md](DNS.md)。

HTTPS 面板在上述虚拟机的启动、登录、状态访问测试中，进程 RSS 约 14–18 MiB、9 个线程。该数字不包括 OpenVPN 进程、代理程序、系统缓存或浏览器，也不是所有出口数量下的固定内存承诺。

后续 debug 的无变化配置读取 Windows 微基准约 29.6 μs/次、1789 B/次、30 次分配；它验证缓存路径的开销，不代表 Linux 网络吞吐。连接并发上限 3，健康检查上限 4；页面隐藏停止轮询；健康探测直接使用 Go HTTP，不启动 curl 子进程。直连模式的代理数据不经过面板。

## 验证边界

- 没有受影响服务器的历史日志，不能确认真实 EIO 的硬件/文件系统根因。本次模拟了可恢复读错、内容损坏与事务失败；永久损坏存储仍需修复服务器，程序会保留运行服务并拒绝不安全重启。
- 没有可用于公开 ACME 签发的用户域名，因此未对公网证书申请和完整的一键生产安装作真实验收。配置生成、事务及服务调度已回归；Xray/HY2 真实客户端端到端吞吐未测量。
- VPN Gate 是志愿服务。真实连通证明本次并非全体失效，不能保证未来始终有符合地区要求的可用节点。候选耗尽会持续退避恢复并保留原因。
- 自动化测试证明对应场景通过，不能证明软件不存在任何未知缺陷。首次上线宜保留旧配置和异机备份。

功能地图及故障分析见 [AUDIT.md](AUDIT.md)，使用和发布见 [README.md](README.md)。

首轮之后继续修复的竞争窗口、恢复逻辑和新增测试见 [DEBUG.md](DEBUG.md)，其中记录了另一轮真实 VPN Gate 连接和自动切换结果。
