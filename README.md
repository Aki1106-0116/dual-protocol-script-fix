# dual-protocol-script-fix

一键部署和管理以下两种组合：

- **VLESS XHTTP + TLS + Caddy 伪装站 + Hysteria2**：两个不同的直连域名。
- **VLESS REALITY Vision + Hysteria2**：REALITY 用 TCP 443，HY2 使用自己的 ACME 证书。

管理入口为 `tui` 和 HTTPS 网页面板。面板继续使用 HY2 域名证书、随机高位端口、随机路径及密码。

本版本已移除 TCP Brutal 和 Mimic 的安装、配置、API、页面及后台管理器。保留 Gecko、全部端口跳跃设置和其他功能。升级代码只识别旧功能以停用其残留，不提供重新启用入口。

## 安装与发布

要求 Debian/Ubuntu、systemd、root、可用的 `/dev/net/tun`。域名解析必须直连 VPS。安全组开放 TCP 80/443、HY2 UDP 单端口或范围、面板 TCP 高位端口。

把**本目录全部内容（含 `.github`、`lib`、`web`、`scripts`）**上传到仓库 `Aki1106-0116/dual-protocol-script-fix` 的根目录。发布后使用以下命令安装：

```bash
bash <(curl -fsSL https://raw.githubusercontent.com/Aki1106-0116/dual-protocol-script-fix/main/install.sh)
```

本地源码安装：

```bash
sudo bash install.sh
```

仅升级面板/后端、保留节点参数：

```bash
# 在解压后的最新版源码目录内，以 root 执行（无需 sudo）
SKIP_NODE_INSTALL=1 FORCE_SOURCE=1 bash install.sh
```

`FORCE_SOURCE=1` 可避免服务器没有 Go 时误用尚未更新的 GitHub Release；存在本地源码时会使用本地源码，必要时下载并校验临时 Go 工具链。

安装器优先使用本地 Go 1.20+ 源码编译或带校验和的 GitHub Release。没有可用 Release 时下载仓库源码，必要时下载并校验临时 Go 工具链。发布 `v*` tag 后 CI 构建 amd64/arm64 的发布包和 SHA-256 文件。服务器运行时不需要 Go、Node.js 或前端构建工具。

原项目目录和本地源代码不会被此新项目修改。服务器升级时复制旧面板路径、密码、出口和绑定状态，停用旧管理服务；旧版本状态保留，便于排查和回退。迁移已启用的旧 HY2 混淆时，保留 `.before-fix` 配置备份后移除该块。不会卸载系统内核或共享 DKMS 软件包。

## 保留的功能

| 类别 | 功能 |
|---|---|
| 部署 | 两种组合、官方 Xray/HY2/Caddy 安装、域名证书、伪装站、可选 Swap |
| 节点 | 名称、指纹、UUID、XHTTP 域名/路径/后端端口、REALITY 地址/SNI/安全换钥 |
| HY2 | 密码、单端口、端口跳跃、固定/随机间隔、Gecko 密码及分片设置 |
| 分享 | 主节点/HY2 URI、TXT、Xray 客户端 JSON、HY2 客户端 YAML/JSON |
| 管理 | 网页配置和组合重装、按组合重启、面板端口/路径/密码/出口上限 |
| 家宽出口 | 按地区创建、独立绑定两种协议、手动切换、停止、自动检测和恢复 |
| SSH | `tui` 菜单、节点管理、状态、日志、面板设置、切换组合和卸载 |

VPN Gate 提供的是志愿者出口，列表里也可能有公共或其他网络；不能保证每个 IP 都是住宅宽带。

## 存储可靠性

- 配置改动使用同目录唯一临时文件、文件同步、原子替换、目录同步和读回验证。
- 已验证的配置保留内存副本和带 SHA-256 的 `.fix-recovery` 文件。密码、私钥及恢复文件权限受限。
- 读取出错会明确显示存储诊断，并尽可能显示最后验证的配置，不再把 I/O 错误误判成“未安装”。
- 重启或修改前先恢复并验证实际磁盘文件。无法读回或写入时保留正在运行的服务，不执行危险重启。
- 节点事务、出口路由、面板设置和 SSH 后端共用配置锁；网页保存带版本校验，避免覆盖另一窗口刚保存的设置。
- 失败回滚会检查旧服务；回滚本身失败时保留备份并报告真实结果。

`input/output error` 是操作系统返回的 I/O 错误。代码可以处理可恢复的文件损坏和短暂读错，但不能修复坏盘、失效云盘、只读文件系统或底层存储故障。备份也在同一服务器上，不能替代异机备份。

## VPN Gate 自动恢复

当前官方列表仍可用。本次对官方返回的 100 个配置进行解析实测，均可规范化，且均包含 SHA-1 签名的共享客户端证书。SHA-1 本身不能直接被认定为历史故障根因：实测默认证书策略能完成 TLS 并到达服务器认证阶段。程序默认保留正常策略，只在确认“本地客户端证书无法加载且摘要过弱”的特定错误时重试兼容选项，仍验证 CA、有效期和服务器用途，不改变系统全局 TLS 设置。

连接流程会清理并限制第三方配置，使用最新列表中的 IP，设置旧服务器所需的加密回退，等待初始化完成，再验证隧道互联网出口。不会执行第三方脚本、插件、嵌套配置或开放管理监听。

- 最多同时连接 3 条出口；每轮最多 6 个候选。
- 健康检查最多 4 条并发，每 10 秒调度；优先检查被协议绑定的出口。
- 进程退出会触发切换；连续两次网络探测失败后自动切换。
- 两个探测站点避免单一站点故障导致全部误报。
- 隧道 DNS 优先 UDP，受阻时回退 TCP，并尝试多个公共 DNS；所有查询留在所选隧道内。手机 UDP DNS 经隧道转为 TCP，错误响应会尝试备用服务；探测使用绑定到隧道的 Go HTTPS 客户端，不反复启动 curl。
- 失败节点冷却 5 分钟；所有候选失败后继续以 20–320 秒退避重试。
- 列表每 5 分钟刷新；切换时较旧列表会先尝试刷新。
- 保持原槽位和端口；指定地区没有可用候选时持续等待该地区恢复。
- 网络命名空间防火墙及套接字绑定阻止隧道掉线时偷偷走 VPS 直连。恢复需要重新建立连接，现有失败连接不会被无缝搬运。

没有任何程序能在志愿服务器全部离线或所在网络封锁 OpenVPN 时“制造”可用节点。面板会保留失败原因并继续恢复，而不是永久停在失败状态。

## 性能与界面

代理直连流量不经过管理面板。家宽模式才增加 SOCKS5/OpenVPN 转发。后台连接、健康检查、DNS 工作和 UDP 目标数量均有限制；无配置变化时不写文件、不重启服务。页面隐藏后停止轮询，正常状态 10 秒更新，连接期间 3 秒更新。前端资源嵌入 Go 二进制，无外部字体/CDN，无模糊特效或持续动画。

页面按“协议与节点 / 家宽出口 / 管理工具”组织，支持桌面和手机、系统深浅色、触控区域、键盘焦点及模态框操作。

## 文件与命令

```bash
tui
tui info
tui status
tui log
systemctl status dual-protocol-script-fix
journalctl -u dual-protocol-script-fix -n 100 --no-pager
```

| 路径 | 用途 |
|---|---|
| `/etc/jb-combo/state.env` | 节点状态，保留原 TUI 兼容路径 |
| `/etc/default/dual-protocol-script-fix` | 面板环境设置 |
| `/var/lib/dual-protocol-script-fix` | 密码、路径、出口和协议绑定 |
| `/usr/local/lib/dual-protocol-script-fix` | 启动器、模块化节点后端、存储辅助程序 |
| `/root/jb-combo` | 分享链接和客户端配置 |
| `/run/dual-protocol-script-fix-config.lock` | 跨进程配置操作锁 |

`uninstall.sh` 仅卸载面板和出口，先把代理切回 VPS 直连，保留节点。节点 TUI 的“彻底卸载”删除节点和面板，保留已经配置的 Swap。

## 测试

```bash
go test ./...
go vet ./...
go test -race ./...                 # 支持 CGO 的 Linux 开发环境
python3 -m unittest discover -s scripts -p 'test_*.py'
bash scripts/test_backend.sh        # 全部服务调用被替换，使用临时目录
for f in *.sh lib/*.sh scripts/*.sh; do bash -n "$f"; done
npm ci
npx playwright install chromium
npm run test:panel                    # 浏览器回归，服务 API 使用固定测试数据
```

独立 Linux 环境可运行真实 netns 集成测试：

```bash
go test -c -o /tmp/dps-integration.test .
sudo env DPS_RUN_INTEGRATION=1 /tmp/dps-integration.test -test.run TestLinuxNamespaceDialAndRestore -test.v
sudo env DPS_RUN_INTEGRATION=1 /tmp/dps-integration.test -test.run TestLinuxStoppedReconnectCannotDeleteReusedNamespace -test.v
```

该测试创建自己的临时 network namespace/veth，验证并发拨号和命名空间恢复，不接触现有代理服务。完整验证结果见 [VALIDATION.md](VALIDATION.md)，故障分析和重构地图见 [AUDIT.md](AUDIT.md)。

来源与许可见 [NOTICE.md](NOTICE.md) 和 [LICENSE](LICENSE)。

本轮后续排错和新增回归记录见 [DEBUG.md](DEBUG.md)。

“有延迟却打不开网页”的 DNS 专项检查、修复及真实故障注入记录见 [DNS.md](DNS.md)。

Web 保存后持续显示节点读取告警的修复、证书复用规则和升级方法见 [NODE_SAVE.md](NODE_SAVE.md)。
