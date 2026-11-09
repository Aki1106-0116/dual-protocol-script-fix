# 功能梳理、故障分析与重构记录

分析对象是本机原项目的 Go 程序、安装/卸载脚本、SSH 包装器、证书同步器、节点安装器固定版本、加固转换器、网页和测试。原节点上游文件的 SHA-256 与安装器固定值一致。没有取得故障服务器的系统日志，因此下面明确区分代码缺陷与待定位的系统原因。

## 原项目完整调用链

1. `install.sh` 检查 Debian/Ubuntu、root、systemd 和 TUN，安装依赖；下载 Release 或构建 Go；下载固定节点上游并用 Python 文本转换器添加 Web/TCP Brutal/Mimic/换钥加固逻辑。
2. 节点安装器选择 XHTTP/HY2 或 REALITY/HY2。XHTTP 让 Caddy 占用 80/443、反代回环 Xray 并提供测速伪装站，HY2 直接读 Caddy 证书。REALITY 由 Xray 占用 443，HY2 用 HTTP-01 ACME，关闭 Caddy。
3. SSH 向导还配置可选 Swap、指纹、名称、UUID/密钥、端口跳跃和混淆；保存 shell 转义后的 `state.env`，导出 URI、客户端 JSON/YAML 和节点信息。
4. Go 服务提供 HTTPS/随机路径/密码登录；路由 API 管理协议绑定；节点 API 调用后端 `web-export`、`web-apply`、`web-reinstall`；面板 API 修改环境/密码/路径并重启；服务 API 判断组合后调用 systemd。
5. VPN Gate CSV 按速度排序，出口槽位对应一个 namespace、veth、OpenVPN 进程及回环 SOCKS5。Xray/HY2 的受管出站指向本地 SOCKS5，拨号发生在选定 namespace。
6. SOCKS5 支持 CONNECT 和 UDP ASSOCIATE；UDP DNS 被转为隧道内 TCP DNS，返回对应 UDP 响应。支持最多 256 个 UDP 目标和受限并发 DNS 工作。
7. 原后台每 10 秒串行检查出口，每 15 秒重写/检查协议路由；出口和绑定写入 JSON，重启后恢复。网页每 4 秒轮询状态和任务。
8. TUI 包含节点分享、UUID、路径、指纹、REALITY 换钥、HY2 密码/端口、重启、组合切换和卸载；外层 `tui` 还提供面板设置、状态和日志。Swap 不在卸载范围内。

按用户追加要求，原来的 TCP Brutal 限速管理器、Mimic 内核/程序安装与服务权限覆盖全部删除，其余功能保留。

## I/O 故障链路

`open ...: input/output error` 表示操作系统的 `EIO`，不能直接归因于 JSON 格式、密码错误或未安装节点。运行中的 Xray/HY2 已把配置载入内存，因此磁盘读取失败时仍可能继续转发。

原代码的明确缺陷是：`detectStack` 把所有读错误返回为 `not_installed`；其他管理 API 每次直接读文件或调用脚本；没有独立的已验证配置视图；面板据此禁止组合重启；配置写入混用原子替换、固定 `.tmp`、直接覆盖和不检查回滚结果。后台与 Web/TUI 没有同一操作锁，容易覆盖刚保存的配置。

修复内容：

- 区分不存在、结构无效和存储错误；按真实入站标签识别组合。
- 内存和校验和恢复副本支持故障时查看已有配置，并显示诊断。
- 文件级同步、原子提交、目录同步和读回验证；不把无法读回的提交报告为成功。
- 重启前验证对应服务所需的实际文件；持久性磁盘错误时保留运行中的服务。
- Bash/Go 操作锁统一，网页版本戳防止旧表单覆盖新设置。
- 节点与 SSH 改动均使用事务；检测回滚失败并保留恢复备份。
- systemd 启动器先恢复/读取面板环境，避免 systemd 自己在环境文件 EIO 时直接吞掉程序启动机会。
- 节点状态和面板环境在更新恢复副本前检查必需字段；拒绝截断状态、重复字段及可执行的 shell 赋值。

仍需在故障服务器检查 `dmesg`、云盘、文件系统、挂载状态和空间/inode。不能把“永久不可写的磁盘”描述成脚本可自动修好；没有历史健康副本也不能凭空恢复私钥。`.fix-recovery` 属于本机恢复，不是异机备份。

## VPN Gate 调查

2026-10-14 读取官方 API，返回 100 个配置；新解析器接受 100 个，拒绝 0 个；100 个客户端证书均检测到 SHA-1 签名。官方连接说明仍有效，并明确指出旧配置地址过期、DDNS 解析和网络过滤都可能造成失败。

本项目存在的确定问题包括：只看到 tun0 地址就当连接就绪；OpenVPN `ProcessState` 被并发读写；恢复时用保存的旧配置覆盖刚拉取的新配置；没有旧服务器加密回退；直接加载第三方配置为 root；没有独立的隧道出站约束；健康检查依赖单站且串行；候选耗尽后永久停在 failed；不会定期刷新列表；状态写入使用共享临时文件；停止与迟到任务会竞争。旧地址或旧证书配置因此可能在升级/恢复后继续失效。

新逻辑会规范化第三方配置、只保留内联证书和受限参数、添加连接隔离、检查初始化完成与实际出口，使用进程退出事件、双站探测、并发调度、冷却和退避恢复。旧 SHA-1 证书兼容策略限定在单个 OpenVPN 进程，不修改系统 OpenSSL 配置。不会跳过 CA、有效期或服务器用途校验。

实际默认策略对照测试到达了 `AUTH_FAILED`，说明那次失败来自服务器认证阶段，并不是客户端 SHA-1 证书无法加载。因此默认不降低证书安全等级，只对确认的本地加载错误启用一次兼容重试。新的真实连接和故障切换测试已通过，仍不能反推没有日志的历史故障只有一个根因。

这些证据支持保留并修复功能，不能证明原用户每次失败都是同一个原因。真实连接结果和 Linux 测试环境见 `VALIDATION.md`。

## 其他修复

- Go 版本要求与 `crypto/ecdh` 对齐，发布流程不再使用不能编译该代码的 Go 1.18。
- 运行时不再依赖巨大字符串转换器或远程下载节点上游；后端由本项目维护，按公共工具、协议服务、事务和菜单拆成模块。
- 网页按嵌入式 HTML/CSS/JS 分离；去掉重复的旧样式、内联脚本和持续动画；适配手机、深浅色、焦点和分区导航。
- 避免 Xray 路由提交后 HY2 失败造成两协议部分更新；兼容迁移上一项目名的受管标记。
- 禁止在未知 HY2 `outbounds` 上叠加重复 YAML；拒绝结构损坏和重复键。
- 不存在的绑定出口保留绑定、等待恢复，不静默回落直连。
- 合并停机/连接生命周期，限制连接和健康检查并发，避免已停止出口被迟到结果复活。
- netns 拨号设超时、在正确线程中建立 socket、恢复线程 namespace；DNS 解析也从对应 namespace 发出。
- 实测部分志愿出口不响应 UDP 53，加入 TCP DNS 回退；在真实隧道中主动阻断 UDP 53 后，解析和 HTTPS 探测仍成功。
- 停止出口时保留槽位直到全部清理结束，关闭已有转发连接，防止同槽位新出口被旧任务误清理。
- VPN 列表和旧节点配置初始化放到后台，HTTPS 登录入口先就绪，避免安装器误判服务未启动。
- 处理 TCP 半关闭，限制未结束的转发；既有 SOCKS5 UDP/DNS 回归测试保留。
- 限制登录状态增长，阻止跨站写请求；不再在解析错误里回显 REALITY 私钥。
- 拒绝通过放宽全局权限解决证书读取问题，仅为服务用户配置 ACL。
- 分享导出先生成完整候选，再逐文件原子提交，并保留正确的最终文件路径。
- 卸载出口前先把代理切回直连，失败则不拆除仍被引用的出口。
- Release 下载验证 SHA-256，二进制先构建/验证候选后替换；新服务启动失败时尝试恢复旧程序/旧服务。

## 模块地图

| 模块 | 职责 |
|---|---|
| `main.go` | CLI、启动、HTTPS、后台生命周期 |
| `api.go` | HTTP 请求解码与业务入口 |
| `node_config.go` / `panel_config.go` | 允许字段、参数验证、事务调度与视图 |
| `storage.go` / `scripts/storage.py` | 同格式恢复副本与可靠提交 |
| `config_lock_*.go` / Bash 锁 | 跨进程串行化与超时取消 |
| `router.go` | 绑定、配置变换、验证和重启回滚 |
| `manager.go` / `health.go` / `state.go` | 出口生命周期、恢复策略、持久化 |
| `vpn_profile.go` / `vpngate.go` | 官方列表和安全连接配置 |
| `tunnel.go` / `netns.go` / `socks5.go` / `dns.go` | 隔离网络、OpenVPN、TCP/UDP 转发、DNS 容错与响应验证 |
| `lib/node-*.sh` | 公共工具、服务配置、完整事务、SSH 菜单 |
| `web/*` | 浏览器视图、样式与交互，无运行时前端依赖 |

## 官方参考

- [VPN Gate 官方连接与故障说明](https://www.vpngate.net/en/howto_openvpn.aspx)
- [VPN Gate 官方 API](https://www.vpngate.net/api/iphone/)
- [OpenVPN 2.6：加密回退、证书安全级别、路由](https://openvpn.net/community-docs/community-articles/openvpn-2-6-manual.html)
- [Hysteria 服务端配置](https://v2.hysteria.network/docs/advanced/Full-Server-Config/)
- [Hysteria 客户端配置](https://v2.hysteria.network/docs/advanced/Full-Client-Config/)
- [Xray REALITY 字段](https://xtls.github.io/en/config/transports/reality.html)
