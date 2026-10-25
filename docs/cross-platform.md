# 跨平台缺陷清单与修复设计

> 适用版本：NatPilot 0.1.0 · 文档日期 2026-10-24
> 目标平台：Windows / macOS / Linux 的**桌面端**、**服务端**与 **Docker**。

本文件记录一次针对「跨平台正确性」的专项审计：缺陷、证据、根因、修复与验证方式。
文中的编号（`CP-xx`）在提交信息与后续 issue 中沿用。

---

## 0. 结论摘要

| 项 | 修复前 | 修复后 |
| --- | --- | --- |
| 六目标编译（linux/darwin/windows × amd64/arm64） | ✅ 能编译 | ✅ 能编译（`vet` 也通过） |
| `go test ./...` 在 Windows | ❌ 1 个用例失败 | ✅ 全绿 |
| 多网卡/VPN 主机上的网关发现 | ❌ 120s 未返回（需 kill） | ✅ 11.5s 有界返回 |
| 本机端口扫描（Windows，222 个监听） | ❌ 58s，Host 全是 `127.0.0.1` | ✅ 6.2s，Host 为真实监听地址/LAN 地址 |
| 映射目标地址（InternalClient） | ❌ 可能是 TUN/链路本地地址 | ✅ 默认路由所在的 LAN 地址 |
| 桌面壳长时间运行 | ❌ 日志 200 行后管道死锁 | ✅ 持续消费 |

---

## 1. 平台能力矩阵（修复后）

| 能力 | Linux | macOS | Windows | Docker(Linux) |
| --- | --- | --- | --- | --- |
| 默认网关 | `/proc/net/route` | `route -n get default` → `netstat -rn` | `route print -4` → `netstat -rn`（**取最小 metric**） | 同 Linux |
| 邻居表 | `/proc/net/arp` → `ip neigh` | `arp -an` | `arp -a`（**形状识别，兼容本地化表头**） | `/proc/net/arp` |
| 本机监听 | `/proc/net/{tcp,udp}*` | `netstat -an` + `lsof` | `netstat -ano` + `tasklist`（**状态列不依赖英文**） | 同 Linux |
| SSDP 发现 | 每块网卡独立 socket + `IP_MULTICAST_IF` | 同左 | 同左 | 建议 host 网络（见 §5） |
| PCP / NAT-PMP | 并行探测，源地址按目标路由选择 | 同左 | 同左 | 同左 |
| 桌面壳 | ✅ | ✅（`open`） | ✅（GUI 子系统 + 子进程无控制台） | — |

---

## 2. P0：导致「装上就用不了」的缺陷

### CP-01 SSDP 只从默认路由网卡发出

- **现象**：装有 VPN / VMware / Hyper-V / Tailscale / ZeroTier 的主机上，路由器永远收不到 `M-SEARCH`。
- **证据**：`Discover()` 在本机 25s 内收到 0 个设备；同一网络里手工发 M-SEARCH 能收到回应。
- **根因**：`SSDPDiscover` 虽然接收 `ifaces` 参数，但函数体内是一段死代码：

  ```go
  for _, ifc := range ifaces { conns = append(conns, nil); _ = ifc; break }
  ```

  实际上只开了一个 `0.0.0.0:0` 的 socket。多网卡主机的组播出口由**组播路由表**决定，
  它跟着默认路由走——在有 TUN 的机器上就是隧道网卡。
- **修复**：每个可用 IPv4 网卡一个 socket，绑定该网卡地址并显式设置 `IP_MULTICAST_IF`：

  | 文件 | 作用 |
  | --- | --- |
  | `internal/proto/igd.go` | `SSDPDiscover(ctx, targets, sources, timeout)`：逐网卡开 socket、逐网卡发送、共享收集通道 |
  | `internal/netutil/mcast_unix.go` | `SetMulticastInterface`（linux/darwin，`unix.SetsockoptInet4Addr`） |
  | `internal/netutil/mcast_windows.go` | 同上（`windows.SetsockoptInet4Addr`） |
  | `internal/netutil/mcast_other.go` | 其它平台的空实现（仍绑定网卡地址） |

  回复是单播回源地址的，因此不需要 IGMP 加入组播组。
- **验证**：本机 5 块可用网卡各发一次，SSDP 阶段不再依赖「只有一块网卡」的假设。

### CP-02 `PrimaryIPv4()` 选到 TUN / 链路本地地址

- **现象**：`host` 留空的规则，映射目标变成 `198.18.0.1`（Clash TUN）或 `169.254.x`（Tailscale/蓝牙），路由器侧不可达。
- **证据**：本机 `PrimaryIPv4() == "198.18.0.1"`，而真实 LAN 是 `192.168.1.8`。
- **根因**：实现是「`Interfaces()` 按名字排序后取第一个 up 且非回环」。Windows 的网卡名是
  `Mihomo`、`Tailscale`、`VMware Network Adapter VMnet1`、`WLAN`…… 排序后 TUN 永远在前。
- **修复**（`internal/netutil/netutil.go`）：

  ```go
  // 分值：默认路由所在网卡 +150 / 私网地址 +40 / up +20 / 链路本地 +0 / 回环 1
  func PickPrimaryIPv4(list []Iface) string
  ```

  纯函数，可对「只在 Windows 存在的网卡集合」做单元测试
  （`platform_policy_test.go: TestPickPrimaryIPv4` 直接复刻本机 7 块网卡）。
  同一函数同时服务于：规则默认目标（`mapper.effectiveHost`）、扫描回退地址、PCP 源地址回退。

- **附带修复**：`RouteGateway()` 增加 5s 记忆化。darwin/windows 的网关查询是**起子进程**，
  而 `effectiveHost` 在每轮 reconcile 中按规则调用——20 条规则过去等于每 60s 起 20 次 `route print`。

### CP-03 Windows / macOS 本机扫描把 Host 硬编码成 `127.0.0.1`

- **现象**：`/ports` 列表所有条目都是 `127.0.0.1`；`--add-open` 建出的规则 `host=127.0.0.1`，
  下发后路由器把它当回环地址 → 映射无效。Linux 行为却不同（返回真实 bind 地址）。
- **证据**：修复前 222 条监听里 `192.168.1.8:9993`、`0.0.0.0:445` 等全部显示为 `127.0.0.1`；
  且 `ports(host,port,proto)` 唯一索引把不同 bind 地址的同端口监听**合并丢失**。
- **根因**：`scanner.scanLocal` 里 `Host: "127.0.0.1"` 是字面量，真实地址被塞进 `Banner` 字段。
- **修复**（`internal/scanner/scanner.go`）：新增纯函数 `normalizeListenerHost(bind, fallback)`

  | bind 形态 | 归一结果 | 理由 |
  | --- | --- | --- |
  | `0.0.0.0` / `::` / `*` / 空 | LAN 地址 | 全地址监听，映射必须给一个可路由地址 |
  | 具体 IPv4 | 原样保留 | 就是服务实际监听的地址 |
  | `::1` | `127.0.0.1` | 回环等价 |
  | 其它 IPv6（含 `%zone`） | LAN 地址 | IGD/PCP 的 InternalClient 只接受 IPv4；顺带合并 Linux 的 `0.0.0.0:80` + `[::]:80` 重复行 |

  `--add-open`、`ports add` 因此得到可用的映射目标；CLI 的 `BANNER` 列仍显示原 bind 地址便于排查。

### CP-04 PCP/NAT-PMP 串行探测，每个死地址 15–20s

- **现象**：`natpilot gateway discover` 在本机运行 >120s 仍未结束；`serve` 启动时的首次发现被 12s 的
  ctx 截断，于是「明明有路由器却报告未发现网关」。
- **证据**：调试日志中每 20s 一条 `pcp announce failed`，候选共 12 个。
- **根因**：三重叠加——① 候选串行遍历；② `PCPClient.exchange` / `NATPMPClient.request`
  的重传循环每次失败都把 deadline **重置**为 `now+timeout/2`，单次探测被放大到 ~3×timeout；
  ③ 候选列表包含 `169.254.x`、`198.18.0.x` 这类一定不会有 NAT 设备的地址。
- **修复**：

  | 位置 | 改动 |
  | --- | --- |
  | `internal/proto/discover.go` | SSDP 与 UDP 探测并发；候选**并行**（信号量 8）；整体受 `ctx` 约束；每候选 `CandidateTimeout`（默认 900ms） |
  | `internal/proto/discover.go` | `CandidateServers()` 重写：默认网关 → 各网卡的网关 → 私网 `/22` 及更细子网的 `.1/.254`；剔除回环/链路本地/`198.18.0.0/15`（RFC2544，Clash/Surge 的 TUN 池）；候选上限 8 |
  | `internal/proto/pcp.go`、`natpmp.go` | 重传不再延长绝对 deadline；剩余时间不足 20ms 直接放弃 |
  | `internal/proto/discover.go` | 失败才 `Close()` provider（原来成功也关） |

- **验证**：`gateway discover` 在本机从「>120s 未完成」变为 **11.5s 有界返回**（SSDP 窗口 5s + 并行探测 ≈2s + 启动开销）。

### CP-05 探测源地址不匹配目标网段

- **现象**：向 `192.168.1.1` 发 PCP 的报文源地址是 `198.18.0.1` / `169.254.83.107`。
- **根因**：`DiscoverOptions.LocalIP` 对所有候选用同一个值（来自 CP-02 的错误选择）。
- **修复**：新增 `netutil.LocalIPv4For(target)`（UDP connect 不发包，问内核「去这个地址用哪个源地址」），
  每个候选各自取源地址；`LocalIP` 仅作为兜底。

---

## 3. P1：功能正确但体验/兼容性受损

### CP-06 桌面壳日志管道死锁

- **现象**：桌面模式跑一段时间后服务端「卡死」，CPU 0%，日志停止。
- **根因**：`cmd/desktop` 把子进程 stdout/stderr 逐行推入 `chan string`（容量 200），
  readiness 检测结束后**没有任何消费者**。攒满 200 行 → goroutine 阻塞 → OS 管道缓冲（64KB）填满 →
  服务端下一次写日志阻塞。
- **修复**：改为常驻 `pump` goroutine，直接把输出写到 `os.Stdout`/`os.Stderr`，永不停读；
  readiness 循环改为轮询 `/api/health`（并用原子标志判断是否已打印 listening 横幅）。

### CP-07 Windows 控制台闪窗 + 桌面壳无 GUI 子系统

- **现象**：桌面模式每次扫描/发现都闪黑框；`natpilot-desktop.exe` 自带一个控制台窗口，关掉它服务就没了。
- **修复**：
  - `internal/oscmd/oscmd_windows.go`：所有外部命令加 `HideWindow + CREATE_NO_WINDOW`；
  - `cmd/desktop/child_windows.go`：拉起的服务端子进程同样不分配控制台；
  - `Makefile` / `build.sh` / `build.ps1`：Windows 目标给桌面壳加 `-H windowsgui`
    （**只对 windows 生效**，其它平台传该 flag 会报错）。

### CP-08 `route print -4` 忽略 Metric，且误读 Persistent Routes

- **现象**：多默认路由（VPN/TUN）时选出的网关取决于输出顺序，而不是内核实际使用的路由。
- **证据**：本机三条默认路由，metric 分别为 35 / 10034 / **0**；修复前返回第一条（35），
  metric 0 的 TUN 路由才是生效路由。
- **修复**（`internal/netutil/netparse.go`）：只接受**活动路由行**（5 列且第 4 列是接口 IP），
  取 `Metric` 最小者；`Persistent Routes` 段（4 列）被自然排除，不需要识别本地化的段标题。
- **回归测试**：`TestParseRoutePrintWindowsPicksLowestMetric`、`...NetstatShape`、`...IgnoresLocalisedOnLink`。

### CP-09 `arp -a` 本地化表头导致接口信息丢失

- **现象**：中文 Windows 的 `arp -a` 打印 `接口: 192.168.1.8 --- 0x1a`，解析器只认 `interface:`，
  于是「主机清单」里所有邻居的 `Iface` 为空。
- **修复**：按**形状**识别段头——包含 `---`（MAC 列永远不会出现连续三个短横线）且左侧含 IPv4 字面量。
  已覆盖英文/中文/德文三种表头（`TestParseWinARPInterfaceHeader`）。

### CP-10 `netstat -ano` 状态列本地化导致 TCP 监听全被丢弃

- **现象**：德语/法语等版本 Windows 上本机扫描返回 0 条 TCP（状态是 `ABHÖREN` / `À L'ÉCOUTE`）。
  中文版恰好仍是 `LISTENING`（已实测），所以问题隐蔽。
- **修复**：监听判定改为「远端端点是通配形式」（`0.0.0.0:0` / `[::]:0` / `*:*`），
  与语言无关；英文 `LISTENING` 仅作为兜底。

### CP-11 CLI 表格按字节截断，中文进程名乱码

- **现象**：`PowerToys.MouseWithoutB�?`、`AppleMobileDeviceProces�?`，tabwriter 列宽也错位。
- **修复**：`truncateCell` 改按 rune 截断（`cli_rules.go`），并补 `truncate_test.go` 做全宽度遍历，
  断言输出始终是合法 UTF-8。

---

## 4. P2：部署与工程化

| 编号 | 问题 | 修复 |
| --- | --- | --- |
| CP-12 | 容器识别只在 cgroup v1 下有效 | `config.inContainer()`：`/.dockerenv` → `/run/.containerenv`(Podman) → cgroup 关键词；删除已失效且无人调用的 `dockerSuggestion` |
| CP-13 | `go test` 在 Windows 失败（用例写死 POSIX 路径） | `serverbin_test.go` 改用 `t.TempDir()`，提示文本按 `/` 归一后比较 |
| CP-14 | `internal/webui/dist/placeholder.html` 缺失 | 重建（README 承诺「新鲜克隆可编译」依赖它满足 `go:embed`） |
| CP-15 | `docker-compose.yml` 写死 `network_mode: host`，Docker Desktop(Windows/macOS) 直接启动失败 | 默认文件改为 **bridge + 发布端口**（全平台可跑）；新增 `docker-compose.hostnet.yml` 供 Linux host 网络 |
| CP-16 | Windows 无对等构建入口 | 新增 `build.ps1`（`-All` / `-FrontendOnly` / `-NoFrontend` / `-Test` / `-Vet`） |
| CP-17 | 本机扫描逐端口 grab banner，Windows 上 58s | 扫描先收集，再 16 并发抓 banner（`scanLocal`），耗时 6.2s |
| CP-18 | 会话密钥由时间种子 LCG 生成 | 改用 `crypto/rand`（`config.randomHex`），保留失败兜底 |

---

## 5. 部署形态与网络注意事项（不变但需明确）

1. **Docker 无法为宿主机映射端口**：容器在 bridge 内时只能看到 docker 网桥。
   Linux 请用 `docker-compose.hostnet.yml`（`network_mode: host`）；Windows/macOS 请直接在宿主机运行二进制。
2. **UPnP 依赖同一广播域**：VLAN、访客网络、AP 隔离会阻断 SSDP。修复后每个网卡都会发 M-SEARCH，
   但跨网段仍然无效（协议限制）。
3. **IPv6 不做端口映射**：IGD v2 / PCP 的 InternalClient 语义与现有引擎都以 IPv4 为中心，
   扫描结果中的 IPv6 监听会被折叠到 IPv4 地址（CP-03）。
4. **macOS 进程名**：`lsof` 非 root 只能看到本用户进程，端口列表完整但部分进程名为空（既有行为，已在 README 说明）。

---

## 6. 如何验证

```bash
# 三平台编译 + 静态检查
GOOS=linux  GOARCH=arm64 go build ./... && GOOS=linux  GOARCH=arm64 go vet ./...
GOOS=darwin GOARCH=arm64 go build ./... && GOOS=darwin GOARCH=arm64 go vet ./...
go build ./... && go vet ./...                      # 宿主（本项目在 windows/amd64 验证）

# 测试（含本地化/多网卡回归用例）
go test ./... -count=1

# 运行期：多网卡主机上应在 ~5s 内完成发现，且扫描 Host 不是 127.0.0.1
natpilot scan local
natpilot gateway discover
```

新增/加强的测试：

| 文件 | 覆盖 |
| --- | --- |
| `internal/netutil/platform_policy_test.go` | `PickPrimaryIPv4`（复刻本机 7 网卡）、`isPrivateIPv4`、路由 metric、中文 `arp -a`、三种语言的接口表头 |
| `internal/scanner/host_normalize_test.go` | `normalizeListenerHost`、`bannerTarget`、德语/英文 `netstat -ano`、通配端点判定 |
| `truncate_test.go` | CJK 截断不产生非法 UTF-8 |
| `cmd/desktop/serverbin_test.go` | 平台无关的路径断言 |

---

## 7. 已知限制 / 后续建议

1. **Windows 网络信息仍依赖外部命令**（`netstat` / `tasklist` / `route` / `arp`）。
   已通过 `CREATE_NO_WINDOW` 消除闪窗，但仍有 ~50–200ms/次的进程开销与解析脆弱性。
   建议后续迁移到 `golang.org/x/sys/windows` 的 `GetExtendedTcpTable` / `GetExtendedUdpTable` /
   `GetIpNetTable2` / `GetIpForwardTable2`：无本地化依赖、无子进程、快一个数量级。
2. **SSDP 需要显式组播接口**：非 linux/darwin/windows 平台（`mcast_other.go`）只能靠绑定源地址，
   在单网卡机器上足够，多网卡需要补平台实现。

3. ~~桌面壳「关窗即退出」~~ → 已按「托盘常驻」重做，见 §8。macOS/Linux 的**单二进制**壳仍只支持
   前台运行（托盘由 Electron 版提供），原因见 §8.3。
4. **端口预留竞态**：`cmd/desktop` 用 `listen(:0)` → `Close()` → 服务端重新 bind，存在被抢占的窗口。
   可改为把已打开的 listener 通过 `ExtraFiles`/继承句柄传给服务端。

5. **`supports()` 按协议能力过滤仍不生效**（两个 `switch` 分支都 `return true`），目前靠下发失败回退兜底。

---


## 8. 桌面版：托盘常驻（CP-19）

### 8.0 结论：桌面版统一用 Electron

曾经在 `cmd/desktop` 里实现过一套 Windows 原生托盘（`Shell_NotifyIconW` + 隐藏窗口 + 消息循环），
但那只覆盖一个平台，与「桌面版三平台行为一致」的目标冲突，也会让同一功能出现两套实现与两套 bug。
**最终方案：托盘能力只由 Electron 提供**，Go 侧只保留一个无托盘的轻量启动器。

| 形态 | 定位 | 托盘 |
| --- | --- | --- |
| `desktop/`（Electron） | **桌面版**，推荐 | ✅ Windows / macOS / Linux 同一套实现 |
| `natpilot-desktop`（Go 单文件） | 可选启动器（不想装 Electron 时） | ❌ 前台运行，`Ctrl+C` 退出 |
| `natpilot serve` | 服务端 / Docker | —（systemd 或容器托管） |

### 8.1 目标行为（Electron）

| 动作 | 结果 |
| --- | --- |
| 关闭窗口 | `close` → `preventDefault()` + `hide()`，服务继续运行 |
| 双击图标（Win/Linux）/ 点菜单栏图标（macOS） | `showWindow()` |
| 托盘菜单「打开控制台」 | `showWindow()` |
| 托盘菜单「在浏览器中打开」 | `shell.openExternal(当前 URL)` |
| 托盘菜单「退出 NatPilot」（macOS 亦可 `⌘Q`） | `app.quit()` → `before-quit` 停服务端子进程 + 销毁托盘 |
| 再次启动 | 单实例锁，已有窗口被唤到前台 |
| 窗口尺寸/位置 | 记忆在 `userData/window-state.json` |
| 非本地链接 | `will-navigate` 拦截并交给系统浏览器 |

### 8.2 三平台实现

| 能力 | Windows | macOS | Linux |
| --- | --- | --- | --- |
| 托盘 | `Tray`（任务栏通知区） | `Tray` + 模板图（菜单栏，自动适配深浅色） | `Tray`（StatusNotifier；缺插件时 Electron 自动回退） |
| 关闭即隐藏 | ✅ | ✅（并 `app.dock.hide()`） | ✅ |
| 任务栏/启动器图标 | `app.setAppUserModelId` | 应用包图标 | `.desktop` + AppImage 图标 |
| 服务端子进程 | `spawn(..., { windowsHide: true })`，随 `before-quit` 结束 | 同左 | 同左 |

### 8.3 为什么不给 Go 单文件壳做托盘

保持 `CGO_ENABLED=0` + 「任意机器交叉编译」是项目的硬约束：

- macOS 的 `NSStatusItem` 需要 Objective-C 运行时（`objc_msgSend`），标准做法是 cgo；
- Linux 的系统托盘需要 StatusNotifierItem（D-Bus 对象 + dbusmenu）或 GTK，前者要自己实现 D-Bus 协议栈，
  后者需要 cgo/GTK 头文件。

Electron 自带这三套实现，因此托盘只在 Electron 里做；Go 壳保持「无依赖、可交叉编译」。
代价是 macОS/Linux 上单文件壳没有托盘——需要托盘就用 Electron 包。

### 8.4 验证

已验证（本机 Windows）：

- `node --check desktop/electron/main.js` 通过；`npm run icons` 能重新生成 4 个托盘图与 512×512 应用图标；
- Go 侧：`natpilot-desktop --no-open` 前台启动、`Ctrl+C` 退出、子进程随之结束；Linux 下由
  `Pdeathsig=SIGTERM`、Windows 下由 Job Object 保证不残留孤儿。

未验证（本环境限制）：

- Electron 的 GUI 行为（托盘点击、隐藏/显示、`⌘Q`）需要在装有 Electron 运行时的机器上
  `cd desktop && npm install && npm start` 实测；`npm run dist` 需在目标平台执行并先构建对应平台的
  `bin/natpilot[.exe]`。
