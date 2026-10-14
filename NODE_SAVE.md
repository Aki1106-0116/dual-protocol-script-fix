# Web 保存后的节点状态告警

## 根因与修复

这条路径存在两个相连的代码缺陷，已在隔离 Linux 环境复现：

1. `NodeConfigService.Apply` 先取得 `/run/dual-protocol-script-fix-config.lock` 的 Go flock，再调用 `web-export`。Bash 的 `config_lock` 不知道父进程已持锁，重复获取同一把锁，等待 30 秒后退出 1。由于服务已有上次正确配置，原代码回退到缓存继续保存，同时记录“节点状态读取失败，展示上次验证的配置”。带 revision 的重装也有同样问题。
2. 原保存返回成功后没有读回新状态、更新缓存及清除上述告警。主页 `/api/status` 只展示已记录的告警，不调用节点导出器，因此保存后这个临时错误会持续显示。直接手动运行 `web-export` 时没有父进程持锁，通常可以成功，这与本次反馈的手动检查未显示错误相符。

现在，持锁期间的导出明确向子进程传递 `DPS_CONFIG_LOCK_HELD=1`；普通只读导出仍自行获取锁。修改和重装前必须读取当前真实状态，禁止使用缓存执行写操作。保存/重装后在释放全局锁前重新读取并验证状态、更新 revision 与缓存，成功时清除旧告警。读回失败会明确提示“配置已写入，但读回校验失败”，不会宣称保存成功或把未验证结果放进缓存。只读界面仍可展示上次验证的配置，保留原故障恢复能力。

导出过程也使用进程组取消，避免超时后遗留等待锁的 Bash/flock 子进程。错误提示保留后端标准错误的具体原因，不再只显示 `exit status 1`。

这说明的是已复现的代码缺陷；没有收到服务器完整日志，不能用它排除服务器上所有其他读取故障。

## 证书复用与 XHTTP 配置修复

- 未修改 HY2/XHTTP 域名的普通保存，不进入对应的等待签发流程，不删除其证书缓存。REALITY+HY2 保持原 ACME 存储目录和自动续期；XHTTP+HY2 保持 Caddy 证书管理。
- 修改 HY2 域名只等待新 HY2 域名证书；修改 XHTTP 域名只等待新 XHTTP 域名证书。比较前统一域名大小写，避免大小写差异被当成新域名。
- Caddy 配置内容没变且服务正常时，不重启 Caddy。XHTTP 路径、后端端口或域名变化时仍应用新配置；已停机的 Caddy 仍会启动。
- 自动续期保留。证书过期、缓存损坏或丢失时，证书管理器仍可进行必要的恢复/续期；没有通过关闭 ACME 来实现复用。

本轮还发现 Caddy 配置生成器的 heredoc 中混入了 Python 写入命令，导致候选配置没有正确提交。现将生成、真实 Caddy 校验和原子写入分开：用 `--adapter caddyfile` 校验候选文件，校验成功才提交，失败保留旧文件并进入事务回滚。

## 验证

- Windows `go test ./...`、`go vet ./...` 通过。
- Ubuntu 24.04 / Go 1.22.2 / GCC 13：`DPS_RUN_INTEGRATION=1 go test -race ./...` 通过（102.504 秒，含完整回归与新增 Bash/Caddy 集成）。
- Windows Git Bash 与 Linux 的服务事务回归通过；13 项 Python 存储测试通过；所有 Bash 脚本语法检查通过。
- Linux amd64、arm64 均编译通过；arm64 未在硬件运行。

新增 `node_backend_integration_test.go` 直接调用真实 Bash 模块及真实 flock、状态持久化、JSON 导出、配置生成器与 Caddy 2.6.2 校验器，验证：

1. 不传父进程持锁信息的导出确实阻塞，传递后保存正常。
2. REALITY/XHTTP 保存后，名称、密码、缓存和 revision 均更新，主页使用的节点告警记录被清除。
3. 未改域名时，不进入签发等待，已有有效 PEM 证书和私钥字节保持不变；Caddy 配置不变时不重启。
4. 单独改变 HY2/XHTTP 域名时，只进入相应域名的签发分支。
5. 导出失败时，即使有旧缓存，也拒绝修改；提交后导出失败时不能报告成功。

测试使用独立临时目录；代理服务启动与公网 ACME 调用使用替身，没有对不属于用户的域名发起公网签发。Go→Bash 锁、状态读写、配置生成和 Caddy 校验为真实执行。新增测试已加入 GitHub CI，避免只测试不加锁的导出替身再次漏检。

证书管理行为参考 [Hysteria 服务端 ACME 配置](https://v2.hysteria.network/docs/advanced/Full-Server-Config/) 与 [Caddy 自动 HTTPS](https://caddyserver.com/docs/automatic-https)。

## 现有服务器升级

将最新版源码 ZIP 上传服务器并解压，进入包含 `install.sh`、`main.go`、`lib/` 的目录，以 root 执行：

```bash
SKIP_NODE_INSTALL=1 FORCE_SOURCE=1 bash install.sh
```

这会更新并重启面板/后端，保留当前节点参数，不进入交互式组合重装。首次移除旧版已启用混淆的迁移例外沿用原安装器逻辑。

必须同时更新 Go 二进制与 Bash 模块；只复制 `node-installer.sh` 不会修复 Go 中的重复加锁。`FORCE_SOURCE=1` 防止下载旧 Release 覆盖本次源码。命令无需 sudo，适用于当前已经以 root 登录的服务器。
