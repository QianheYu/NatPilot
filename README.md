# NatPilot

**NAT 端口映射控制台 / NAT Port Mapping Control Plane**

用一个二进制程序管理路由器的端口映射：支持 **UPnP IGD v2（首选）**、**PCP (RFC 6887)**、**UPnP IGD v1** 与 **NAT-PMP (RFC 6886)**，内建主机+端口管理列表、本机/网段端口扫描、策略化自动监控、SQLite 持久化、REST API、SSE 实时日志，以及一个现代化中台风格的 Web 控制台（已内嵌，无需单独部署前端）。

```
┌────────────┐   CLI / REST / Web UI (Vue3 SPA, embedded)
│  NatPilot  │◄──────────────────────────────────────────► 用户
│            │
│  映射引擎   │  IGD v2 → IGD v1 → PCP → NAT-PMP  (自动降级)
│  扫描器     ├───────────────►  路由器 / NAT 网关
│  策略引擎   │
└─────┬──────┘
      │
   SQLite (规则 / 映射 / 网关 / 主机 / 扫描 / 策略 / 计划 / 用户 / 日志)
```

---

## 目录

- [功能特性](#功能特性)
- [快速开始](#快速开始)
- [命令行](#命令行)
- [部署方式](#部署方式)
- [配置](#配置)
- [协议支持与降级策略](#协议支持与降级策略)
- [端口生命周期](#端口生命周期)
- [自动监控](#自动监控)
- [REST API](#rest-api)
- [项目结构](#项目结构)
- [开发](#开发)
- [常见问题](#常见问题)

---

## 功能特性

> **需求编号说明（追溯用）**
>
> 本章沿用最初需求清单的编号。其中 **需求 2、需求 3 在本仓库既没有对应条目、也没有实现**——这是
> 需求清单自身的空缺，不是漏写的章节。为避免后来者误以为文档缺页，这里显式登记编号映射：
>
> | 编号 | 对应内容 |
> | --- | --- |
> | 需求 1 | 协议支持与降级（本章 §1） |
> | 需求 2、3 | **空缺**（需求清单未定义，仓库无实现） |
> | 需求 4、5、6 | 主机与端口管理（本章 §2） |
> | 需求 7、8、9、10、11 | 界面与部署（本章 §3） |
> | 需求 12 | 扩展的自有功能（本章 §4） |

### 1. 协议（需求 1）

| 协议 | 说明 | 默认优先级 |
| --- | --- | --- |
| **UPnP IGD v2** | `WANIPConnection:2`，支持 `AddAnyPortMapping`（让网关自动分配可用外部端口）与租约 | 1（首选） |
| **PCP** (RFC 6887) | 完整的端口控制协议，MAP/ANNOUNCE，显式租约与删除 | 2 |
| **UPnP IGD v1** | 传统 `WANIPConnection:1` / `WANPPPConnection:1`，兼容老路由器 | 3 |
| **NAT-PMP** (RFC 6886) | 兼容性最好，仅 TCP/UDP 单端口映射 | 4 |

- **不冲突地优先新协议**：按配置的顺序依次尝试，首个成功者即被采用，其余仅作为失败回退；可在「网关与协议」页固定首选网关。
- **旧协议兼容**：IGD v1 / NAT-PMP 作为兼容层保留，老设备同样可用。
- **能力协商**：刷新网关时读取设备描述与 `MAPPING` 能力，能力集（`map-tcp` / `delete` / `enumerate` / `add-any-port` …）会展示在 UI 中。

### 2. 主机与端口管理（需求 4、5、6）

- 管理列表按 `主机 + 端口 + 协议` 唯一，支持 **添加 / 删除 / 修改 / 标签 / 备注 / 启用停用**。
- **只有「启用」的规则**才会被下发到网关；停用会主动撤销映射。
- 端口扫描分 **本机** 与 **网段（CIDR）/ 局域网** 两类，除扫描范围外行为一致。
- 扫描结果带完整状态：`已添加` / `已开启` / `已关闭` / `未添加` / `源已删除`，由用户在界面上选择加入管理列表、忽略、备注或删除，并可批量操作。
- **手动触发**与**自动监控**两个独立开关；自动监控需先配置至少一组默认策略（见下）。

### 3. 界面与部署（需求 7、8、9、10、11）

- Web 控制台基于 **Vue 3 + Vite**，产物内嵌进 Go 二进制（`internal/webui/dist`），单文件即可运行。
- 前端是独立子项目 `frontend/`，可单独开发（`npm run dev`，代理 `/api` 到后端）。
- 布局为中台风格：左侧分组导航 + 顶部状态栏 + 卡片/表格/标签/状态徽章，暗色优先，支持亮色主题与响应式。
- 部署形态：**桌面 GUI**（免登录）、**服务端 / Docker（Web，需登录）**。
- 登录：启用后使用加密 Cookie 会话 + bcrypt 密码 + JWT API 令牌；桌面模式 `auth_required=false` 自动免登录。

### 4. 扩展的自有功能（需求 12）

- **网关管理**：多网关发现、首选网关、按能力回退、枚举网关上的既有映射（含其它程序创建的）。
- **策略引擎**：按端口范围/排除项/协议/目标类型匹配，命中后自动「添加 / 忽略 / 观察 / 删除」并打标签 → 自动监控的基础。
- **计划任务**：5 段 cron，可定时执行「扫描 / 同步映射 / 清理失效映射 / 网关探测」。
- **冲突检测**：多条启用规则请求同一外部端口时标记冲突并跳过，绝不覆盖已有映射。
- **演练模式（dry-run）**：只记录不真正下发，用于上线前验证规则。
- **租约续期**：临近过期自动续约；规则变更会先撤销旧映射再下发新映射。
- **主机清单**：读取 ARP 邻居表 + 接口信息，识别网关/本机/对端与厂商。
- **日志与审计**：结构化系统事件（SSE 实时推送）+ 安全审计流水。
- **导入导出**：规则/策略/计划/主机 JSON 导出、CSV 导出、JSON 导入。
- **API 浏览器**：控制台内置接口目录，可在线试运行每个 endpoint。

---

## 平台支持

三项依赖操作系统接口的功能（默认网关检测、ARP 邻居发现、本机端口枚举）都提供了
各平台的原生实现，不再只有 Linux 可用：

| 功能 | Linux | macOS | Windows |
| --- | --- | --- | --- |
| 默认网关检测 | `/proc/net/route` | `route -n get default` → `netstat -rn` | `route print -4` → `netstat -rn` |
| ARP 邻居发现 | `/proc/net/arp` → `ip neigh` | `arp -an` | `arp -a` |
| 本机端口枚举 | `/proc/net/{tcp,udp}*` + `/proc/<pid>/fd` | `netstat -an -p tcp\|udp` + `lsof` 补全进程名 | `netstat -ano` + `tasklist` |


各平台的解析都**不依赖系统语言**，也不假设「只有一块网卡」：

- **默认路由**取 IPv4 路由表中 **Metric 最小**的活动路由（VPN/TUN 会产生多条默认路由）；
- **`arp -a`** 按形状识别段头，中/英/德文（`接口:` / `Interface:` / `Schnittstelle:`）均可；
- **`netstat -ano`** 用「远端端点为通配」判断监听，不依赖本地化的状态名（`ABHÖREN` / `À L'ÉCOUTE` / `LISTENING`）；
- **SSDP** 对每块可用网卡各开一个 socket 并设置 `IP_MULTICAST_IF`，VPN/VMware/Hyper-V 环境下的路由器仍能被发现；
- **本机地址**取「默认路由所在网卡」的地址，而不是枚举顺序里的第一个（避免选到 TUN / `169.254.x`）；
- 扫描结果中的 IPv6 监听会折叠到对应 IPv4 地址，`0.0.0.0` / `::` 折叠到 LAN 地址，因此建出的规则可直接用于映射。

完整的缺陷清单、证据与修复设计见 [`docs/cross-platform.md`](docs/cross-platform.md)。

构建矩阵（`make cross` / `./build.sh --all` / `.\build.ps1 -All` 会为每个目标产出一个**开箱即用目录**）：

| 目标 | 服务端 / Web |
| --- | --- |
| linux/amd64 · linux/arm64 | ✅ |
| darwin/amd64 · darwin/arm64 | ✅ |
| windows/amd64 · windows/arm64 | ✅ |

> 桌面版（窗口 + 系统托盘）由 `desktop/` 的 Electron 应用单独打包，**只能为本机平台出包**
> （跨平台打包需要对应平台的工具链），因此不在上面的 Go 构建矩阵里。

产物结构如下，每个目录都是一个完整可运行的分发包：

```
dist/windows-amd64/
  natpilot.exe           服务端 + 内嵌 Web 控制台
dist/linux-arm64/
  natpilot
```

> 每个目录里只有一个二进制，单独拷走即可运行，它自带完整的 Web 控制台。
> 想要**窗口 + 系统托盘**体验请用下文单独打包的 Electron 桌面版。

### 桌面版：窗口 + 系统托盘（推荐）

桌面能力只由 `desktop/` 下的 Electron 应用提供（Windows / macOS / Linux 同一套实现），
Go 侧只发布服务端：需要窗口与托盘就用 Electron 版，服务器环境直接 `natpilot serve` + 浏览器。

```
desktop/release/win-unpacked/
  NatPilot.exe              双击运行：开窗口、驻留托盘
  resources/bin/natpilot.exe 内置服务端（打包时自动带上）
```

行为要点：

- 窗口右上角关闭 → **只隐藏到托盘**，后台服务继续运行。
- 托盘图标双击 → 重新打开窗口；托盘菜单含「打开控制台 / 系统设置 / 在浏览器中打开 / 重启服务 / 打开数据目录 / 退出 NatPilot」。
- **只有托盘菜单里的「退出 NatPilot」才会真正退出**并停止后台服务。
- 运行配置在窗口内的「系统设置」页修改；需要重启才生效的项（监听端口、TLS 等）用托盘「重启服务」。
- 托盘菜单还提供**开机自启**开关（登录时只驻留托盘、不弹窗）和**检查更新**（对比最新发布版本）。
- 重复启动不会开第二份，现有窗口会被唤到前台；桌面壳即使被强杀，服务端子进程也会自行退出，不残留孤儿进程。

构建与打包见 [`desktop/README.md`](desktop/README.md)。

几点说明：

- **进程名归属**：macOS 上 `lsof` 非 root 只能看到当前用户的进程，因此端口列表始终完整，
  但部分条目的进程名可能为空；Windows 的 `netstat -ano` 会给出 PID，再由 `tasklist` 解析成进程名。
- **权限**：枚举其它用户的进程需要相应权限（Linux 读 `/proc/<pid>/fd` 同理）。
- 各平台解析逻辑都有单元测试（使用真实命令输出样本），因此无需在对应系统上即可验证。

## 快速开始

### 依赖

| 组件 | 版本 | 说明 |
| --- | --- | --- |
| Go | ≥ 1.26 | 构建后端 |
| Node.js | ≥ 18 | 仅构建前端时需要 |

**不需要 C 编译器。** SQLite 使用纯 Go 驱动 `modernc.org/sqlite`，因此 `CGO_ENABLED=0`，
编译产物是静态二进制，且**所有目标平台都能从任意一台机器交叉编译**，无需交叉工具链。

> 依赖版本由 `go.mod` / `go.sum` 精确锁定，`go build` 会自动按需下载。
> 首次构建前端后，其产物会被 `go:embed` 打进二进制。

### 构建并运行

```bash

# 1. 构建前端并编译二进制（推荐）
make all            # 等价于 make frontend && make build
.\build.ps1         # Windows 等价入口（PowerShell，见下方「开发」）

# 交叉编译全部六个目标（无需交叉工具链）
make cross          # 产物在 dist/

# 2. 运行（服务端模式，默认 0.0.0.0:8787，需登录）
./bin/natpilot --auth true serve
# 首次启动会打印随机生成的管理员密码，形如：
#   NatPilot: generated administrator password for "admin": xxxxxxxxxxxxxxxx

# 3. 打开控制台
#    http://<本机IP>:8787
```

桌面模式（本机使用、免登录）：

```bash
NATPILOT_DESKTOP=1 ./bin/natpilot --host 127.0.0.1 --port 8787 serve
# 端口写 0 则让系统自动分配，serve 会打印实际监听地址
```

---

## 命令行

```
natpilot [全局参数] <命令> [命令参数]

命令:
  serve        启动 HTTP 服务（API + Web UI）与映射引擎
  rule         管理主机/端口映射列表  (list|add|set|rm|enable|disable|apply|tag)
  ports        查看与处理发现的端口    (list|add|ignore|watch|delete|clear)
  scan         执行端口扫描           (local | cidr <CIDR>)
  gateway      网关探测与查看         (list|discover|preferred|mappings)
  mapping      映射状态与操作         (list|reconcile|cleanup|external-ip)
  policy       策略管理               (list|add|rm|enable|disable)
  schedule     计划任务               (list|add|rm)
  user         账号管理               (list|add|rm|passwd|token)
  events       系统事件与审计         (list|clear)
  config       配置查看/初始化        (print|path|init)
  version      版本信息
  help         帮助

全局参数:
  --config PATH     配置文件（YAML 或 JSON）
  --data-dir PATH   数据目录（默认 ~/.natpilot）
  --db PATH         SQLite 路径
  --host ADDR       serve 监听地址（默认 0.0.0.0）
  --port N          serve 监听端口（默认 8787）
  --mode MODE       auto|web|server|desktop（默认 auto）
  --auth BOOL       是否要求登录
  --log-level LVL   debug|info|warn|error
  --log-format FMT  text|json
  --json            机器可读输出
```

### 常用示例

```bash
# 新增一条启用的映射（保存后立即下发到网关）
natpilot rule add --host 127.0.0.1 --port 8080 --protocol tcp \
  --name "Web 控制台" --tags web,dev --note "对外访问用" --enable

# 查看列表（含实时映射状态）
natpilot rule list
natpilot --json rule list          # JSON 输出，便于接入脚本

# 停用 / 启用（停用会撤销网关上的映射）
natpilot rule disable 3
natpilot rule enable 3

# 本机扫描（不加 --ports 时列出所有监听端口）
natpilot scan local
natpilot scan local --ports 1-1024 --add-open       # 扫描后自动加入管理列表

# 网段扫描
natpilot scan cidr 192.168.1.0/24 --ports 22,80,443,8000-8100

# 端口发现结果处理
natpilot ports list --status not_added
natpilot ports add 12 13 --tags nas
natpilot ports ignore 14
natpilot ports clear --status closed,source_gone   # 清理失效记录

# 网关
natpilot gateway discover
natpilot gateway list
natpilot gateway mappings           # 枚举网关上的映射（IGD）

# 映射
natpilot mapping list
natpilot mapping reconcile
natpilot mapping external-ip

# 策略与计划
natpilot policy add --name "开发端口" --ports 3000-3010,8080 \
  --action add --target local --enable --tags dev
natpilot schedule add --kind scan --cron "*/30 * * * *" --ports 1-1024

# 账号
natpilot user list
natpilot user add --username ops --role operator    # 未给密码则随机生成并打印
natpilot user passwd ops
natpilot user token 1                               # 生成 JWT API 令牌

# 日志
natpilot events list --level warn --limit 50
```

---

## 部署方式

### A. 服务端 / Web

```bash
./bin/natpilot --auth true --port 8787 --host 0.0.0.0 serve
```

反向代理（Nginx）示例：

```nginx
location / {
    proxy_pass         http://127.0.0.1:8787;
    proxy_set_header   Host $host;
    proxy_set_header   X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_buffering    off;          # SSE 实时日志需要
    proxy_read_timeout 3600s;
}
```

### B. Docker


```bash
# 通用（bridge + 发布端口，Windows / macOS / Linux 均可启动）
cp .env.example .env      # 修改 NATPILOT_ADMIN_PASSWORD
docker compose up -d --build

# Linux 且需要管理宿主机端口映射（host 网络，能发现真实路由器）
docker compose -f docker-compose.hostnet.yml up -d --build
```

> **网络模式**：NAT 映射必须由**真实 LAN 内的主机**发起。`docker-compose.yml` 默认是 bridge + 发布端口，
> 因此所有 Docker 宿主都能启动，但容器只能看到 Docker 网桥，无法发现路由器、也无法为宿主机映射端口。
> 需要后者时请用 Linux 的 `docker-compose.hostnet.yml`（`network_mode: host`）——Docker Desktop
> （Windows/macOS）不支持 host 网络，请在宿主机直接运行二进制。

```bash
# 不使用 compose
docker build -t natpilot:latest .
docker run -d --name natpilot --network host \
  -v natpilot-data:/var/lib/natpilot \
  -e NATPILOT_ADMIN_PASSWORD=change-me -e NATPILOT_AUTH=true \
  natpilot:latest serve
```


### C. 桌面版（Electron，跨平台，免登录）

桌面版就是 `desktop/` 里的 Electron 应用，**三平台共用同一套窗口 + 托盘实现**：

```bash
# 1) 先构建服务端二进制（Electron 从 ../bin 取）
make build            # 或: .\build.ps1 -NoFrontend

# 2) 开发运行
cd desktop && npm install && npm start

# 3) 打包安装包（electron-builder，需在目标平台上执行）
npm run dist:win      # release/NatPilot Setup x.y.z.exe
npm run dist:mac      # release/NatPilot-x.y.z.dmg
npm run dist:linux    # release/NatPilot-x.y.z.AppImage / .deb
```

也可以一键「构建 Go 二进制 + 打包」：`make desktop`（Windows 用 `.\build.ps1 -Electron`）。

**托盘常驻语义**：

- 关闭窗口**不会**停止映射引擎，程序继续驻留在托盘（macOS 为菜单栏）；
- 只有**托盘菜单 →「退出 NatPilot」**（macOS 亦可用 `⌘Q`）才会真正退出，同时停掉后台服务并释放端口；
- 托盘菜单还提供「打开控制台」「在浏览器中打开」；双击图标（Windows/Linux）重新打开窗口；
- 再次启动不会开第二份，已有窗口会被唤到前台；
- 退出或崩溃时后台服务一定会被结束，不留孤儿进程占用端口。

详见 [`desktop/README.md`](desktop/README.md)。

#### 不想装 Electron 时

直接用服务端即可：它自带完整的 Web 控制台，浏览器访问就是同一套界面（桌面模式下免登录）。

```bash
./bin/natpilot --host 127.0.0.1 --port 8787 serve   # 然后浏览器打开 http://127.0.0.1:8787
```

> 为什么托盘只在 Electron 里做：macOS 的 `NSStatusItem` 需要 Objective-C 运行时，Linux 的系统托盘需要
> StatusNotifierItem（D-Bus）或 GTK，两条路都绕不开 cgo 或自研协议栈，会破坏「`CGO_ENABLED=0` +
> 任意机器交叉编译」。Electron 自带这三种平台托盘实现，所以桌面版统一走 Electron。

### D. systemd（Linux 服务）

```ini
# /etc/systemd/system/natpilot.service
[Unit]
Description=NatPilot NAT port mapping console
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=natpilot
Environment=NATPILOT_DATA_DIR=/var/lib/natpilot
Environment=NATPILOT_AUTH=true
Environment=NATPILOT_LOG_LEVEL=info
ExecStart=/usr/local/bin/natpilot serve --host 127.0.0.1 --port 8787
Restart=on-failure
RestartSec=3
# 需要绑定低端口或使用 NAT-PMP/SSDP 时可放开:
# AmbientCapabilities=CAP_NET_BIND_SERVICE CAP_NET_RAW

[Install]
WantedBy=multi-user.target
```

```bash
sudo useradd -r -s /usr/sbin/nologin natpilot
sudo mkdir -p /var/lib/natpilot && sudo chown natpilot:natpilot /var/lib/natpilot
sudo systemctl daemon-reload && sudo systemctl enable --now natpilot
```

---

## 配置

配置优先级：**内置默认值 < 配置文件 < 环境变量 < 命令行参数 < 界面保存的设置（SQLite）**。

```bash
natpilot config init ./natpilot.yaml    # 生成带注释的示例配置
natpilot config print                   # 打印当前生效配置（密钥脱敏）
```

```yaml
app:
  data_dir: /var/lib/natpilot
http:
  host: 0.0.0.0
  port: 8787
  mode: auto            # auto|web|server|desktop
database:
  path: /var/lib/natpilot/natpilot.db
scanner:
  max_concurrency: 512
  timeout_ms: 800
  default_ports: "1-1024,3000,3306,5432,6379,8000-8100,8080,8443,9000,9090,27017,32400"
  grab_banners: true
  enable_lan_scan: true
  auto_monitor_local: false      # 启动时是否开启本机自动监控
  auto_local_policy: ""          # 自动监控本机时使用的策略（名称或数字 ID，空=全部启用策略）
  auto_interval_seconds: 300
  auto_apply_policy: true
  auto_add_open: false           # 扫描到的开放端口是否自动建规则
  auto_add_ports: ""             # 限定自动建规则的端口（如 "80,443,8000-8100"，空=全部）
  rate_limit_pps: 0              # 探测速率上限（包/秒），0=不限速
mapper:
  enabled: true
  dry_run: false                 # 演练模式
  protocols: [igd2, igd1, pcp, natpmp]
  default_lease_seconds: 86400
  max_lease_seconds: 604800      # 租约上限，超过会被夹紧到该值
  default_protocol: tcp          # 规则未写协议时的默认值（tcp/udp）
  discover_interval_seconds: 300
  reconcile_interval_seconds: 60
  conflict_policy: skip          # 端口冲突策略：skip|rename|override|error
  external_ip_check: true
  allow_external_port_range: true
auth:
  enabled: false
  require_for_web: true
  session_ttl_hours: 72
log:
  level: info
  format: text
  max_entries: 5000
```

环境变量：`NATPILOT_DATA_DIR`、`NATPILOT_DB`、`NATPILOT_HOST`、`NATPILOT_PORT`、`NATPILOT_MODE`、`NATPILOT_AUTH`、`NATPILOT_ADMIN_USER`、`NATPILOT_ADMIN_PASSWORD`、`NATPILOT_LOG_LEVEL`、`NATPILOT_LOG_FORMAT`、`NATPILOT_SESSION_SECRET`、`NATPILOT_SERVER`、`NATPILOT_DESKTOP`。

界面「系统设置」保存的项目会写入 SQLite `settings` 表并在启动时重新应用（列表类配置如 `mapper.protocols` 用逗号分隔字符串）。

---

## 协议支持与降级策略

```
发现阶段
  ├─ SSDP M-SEARCH (IGD v2 / IGD v1 / WANIPConnection / rootdevice)
  │    └─ 拉取 device description → 解析服务 → 选最优 WANIPConnection
  └─ PCP ANNOUNCE / NAT-PMP 公网地址请求（默认网关 + 常见路由器地址）

下发阶段（每条规则独立尝试）
  按 protocols 顺序逐个网关尝试 AddMapping
     成功 → 记录 provider / 外部 IP / 外部端口 / 租约
     失败 → 记录失败原因，自动回退下一个协议/网关
```

- IGD v2 且未指定外部端口时使用 `AddAnyPortMapping`，由网关分配可用端口；若网关未实现该动作会自动退化为 `AddPortMapping`。
- PCP/NAT-PMP 无法枚举既有映射，`gateway mappings` 仅对 IGD 有效。
- 外部端口冲突（`conflict_policy`）：多条启用规则请求同一外部端口时按策略处理——
  `skip`（默认）保留最小 ID、其后的规则标记为 `冲突` 且不下发；`error` 同 `skip` 但把冲突当失败上报；
  `override` 让最后创建的规则胜出；`rename` 为后来者自动分配一个空闲外部端口。
  任何策略都不会覆盖网关上已经在工作的映射。

---

## 端口生命周期

```
扫描发现
   │
   ├── 命中策略且策略为「自动添加」 ──► 已添加 (added)  ─┐
   ├── 命中策略且策略为「忽略」     ──► 已关闭 (closed)  │
   ├── 规则已存在                   ──► 已添加 (added)  ├─► 用户在界面决策
   └── 无匹配                       ──► 未添加 (not_added) ┘
                                          │
                        规则被删除 ──► 已开启 (open) / 记录被清理
                        端口不再监听 ──► 已关闭 (closed)
                        规则仍在但源消失 ──► 源已删除 (source_gone)
```

界面状态与含义：

| 状态 | 含义 |
| --- | --- |
| **已添加** | 已存在于映射管理列表（规则存在） |
| **已开启** | 端口在监听，但未被管理 |
| **已关闭** | 曾经发现，当前未监听 |
| **未添加** | 正在监听且未被管理，等待用户决策 |
| **源已删除** | 规则仍存在，但对应的源端口已消失 |

---

## 自动监控

手动触发与自动监控是**两个独立开关**：

- **手动**：在「端口发现」页点击「开始扫描」，或在 CLI 执行 `natpilot scan …`。
- **自动监控**：满足 **至少一条启用的默认策略** 后，在「策略与监控」页或 `POST /api/monitor/start` 开启。开启后按固定间隔扫描本机（以及 `scanner.auto_scan_cidr` 配置的网段），并按策略自动添加/忽略/标记。

策略示例（也可用界面创建）：

```bash
# 3000-3010、8080 端口自动加入映射列表并打 dev 标签
natpilot policy add --name "开发端口自动映射" --ports 3000-3010,8080 \
  --action add --target local --enable --tags dev

# 22 / 3389 永不添加，直接忽略
natpilot policy add --name "管理端口保护" --ports 22,3389 \
  --action ignore --target any --priority 100 --enable
```

---

## REST API

统一前缀 `/api`，JSON 交互。认证方式（三选一）：会话 Cookie、`Authorization: Bearer <会话或 JWT>`、`X-API-Key: <JWT>`；未启用鉴权时直接放行。

主要端点：

| 分组 | 端点 |
| --- | --- |
| 元信息 | `GET /health`、`GET /meta`、`GET /stats`、`GET /dashboard` |
| 认证 | `POST /auth/login`、`POST /auth/logout`、`GET /auth/me` |
| 规则 | `GET/POST /rules`、`GET/PUT/PATCH/DELETE /rules/{id}`、`POST /rules/{id}/toggle`、`POST /rules/{id}/apply`、`POST /rules/bulk` |
| 端口 | `GET /ports`、`POST /ports/add`、`POST /ports/{id}/action`、`POST /ports/bulk`、`POST /ports/promote` |
| 扫描 | `GET/POST /scans`、`GET /scans/{id}/results`、`POST /scans/{id}/cancel`、`POST /scans/preview` |
| 策略 | `GET/POST /policies`、`PUT/DELETE /policies/{id}` |
| 监控 | `GET /monitor`、`POST /monitor/start`、`POST /monitor/stop` |
| 网关 | `GET /gateways`、`POST /gateways/discover`、`POST /gateways/{id}/preferred`、`GET /gateways/mappings`、`GET /external-ip` |
| 映射 | `POST /mapper/reconcile`、`POST /mapper/cleanup` |
| 主机 | `GET /hosts`、`POST /hosts/scan` |
| 计划 | `GET/POST /schedules`、`PUT/DELETE /schedules/{id}` |
| 系统 | `GET /system/info`、`GET/PUT/PATCH /settings`、`GET /events`、`GET /audit`、`GET /logs/stream`(SSE)、`GET /export`、`POST /import` |
| 用户 | `GET/POST /users`、`PUT/DELETE /users/{id}`、`POST /users/{id}/token`、`POST /account/password` |

示例：

```bash
# 登录取 Cookie
curl -c cj.txt -X POST http://127.0.0.1:8787/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"你的密码"}'

# 新增规则
curl -b cj.txt -X POST http://127.0.0.1:8787/api/rules \
  -H 'Content-Type: application/json' \
  -d '{"host":"127.0.0.1","port":8080,"protocol":"tcp","name":"web","enabled":true}'

# 实时日志（SSE）
curl -N -b cj.txt http://127.0.0.1:8787/api/logs/stream

# 使用 API 令牌（无 Cookie 场景）
curl -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8787/api/rules
```

> 控制台内置「API 浏览器」页，可直接查看并试运行全部端点。

---

## 项目结构

```
.
├── main.go                  CLI 入口与命令分发
├── cli_rules.go             rule / ports / scan 命令
├── cli_more.go              gateway / mapping / policy / schedule / user / events
├── cli_util.go              密码、YAML、配置初始化等工具
├── version.go               版本元信息（ldflags 注入）
├── cmd/desktop/main.go      可选桌面壳（拉起服务 + 打开窗口）
├── internal/
│   ├── api/                 REST 路由、处理器、中间件、SSE
│   ├── app/                 组装各子系统 + 后台循环 + 调度作业
│   ├── auth/                登录、会话、JWT、bcrypt、锁定
│   ├── bus/                 事件总线 + 日志桥接 + 数据清理
│   ├── config/              配置加载/校验/持久化覆盖（反射支持嵌套键）
│   ├── logging/             分级分类日志 + Hook（供事件持久化/SSE）
│   ├── mapper/              网关发现、规则→映射 reconcile、租约、冲突、清理
│   ├── model/               领域模型与 SQL 编解码类型
│   ├── netutil/             接口/CIDR/端口规格/ARP/厂商/OUI/探测
│   ├── proto/               PCP、NAT-PMP、IGD v1/v2、SSDP 发现
│   ├── scanner/             本机监听枚举(/proc)、CIDR 扫描、策略引擎
│   ├── scheduler/           cron 解析 + 计划任务
│   ├── store/               SQLite schema 与各表访问
│   └── webui/               内嵌前端产物 (go:embed)
├── frontend/                独立前端子项目（Vue 3 + Vite）
│   ├── src/views/           总览/规则/端口/扫描/策略/网关/主机/计划/日志/设置/用户/API 浏览器
│   ├── src/components/      Badge / Modal / Switch / TagInput / EmptyState / ChangePassword
│   ├── src/api.js           API 客户端
│   └── vite.config.js       产物直接输出到 ../internal/webui/dist


├── desktop/                 桌面版（Electron：窗口 + 三平台系统托盘）
├── deploy/                  systemd 等示例

├── Dockerfile / docker-compose.yml / docker-compose.hostnet.yml / .env.example
├── Makefile / build.sh / build.ps1
├── docs/cross-platform.md   跨平台缺陷清单与修复设计
└── README.md
```

前端产物落盘到 `internal/webui/dist`，由 `internal/webui/embed.go` 的 `go:embed` 打进二进制；单个可执行文件同时提供 API 与 UI。

> 仓库内保留了一个 `internal/webui/dist/placeholder.html`，用途是：在 **尚未构建前端** 的全新克隆里
> `go:embed` 仍有可嵌入文件，二进制能正常编译，访问页面时展示“前端未构建”的引导页而不是 404；
> 执行一次 `make frontend` 后即被真正的控制台取代。

---

## 开发

```bash

# 后端（无需 CGO：SQLite 驱动是纯 Go）
go build ./...
go test ./...
go vet ./...

# Windows 等价流程（PowerShell）
.\build.ps1 -Test          # go test ./...
.\build.ps1 -Vet           # go vet（宿主 + darwin + linux）
.\build.ps1 -NoFrontend    # 只重编二进制，不重建前端

# 前端热更新（代理 /api 到 127.0.0.1:8787）
cd frontend && npm install --include=dev && npm run dev

# 终端 1：后端
./bin/natpilot --data-dir ./data serve --port 8787
# 终端 2：前端 -> http://127.0.0.1:5173
```

重新打包前端到二进制：`make frontend build`（或 `./build.sh`）。

---

## 常见问题


**Q：扫描/映射提示「未发现网关」？**
1. 装有 VPN / VMware / Tailscale 等多网卡环境时，0.1.0 起会对**每块网卡**发送 SSDP；仍失败请用
   `--log-level debug` 查看逐网卡、逐候选的探测日志（详见 `docs/cross-platform.md`）。
2. 确认设备与路由器在同一广播域（VLAN/访客网络会阻断 SSDP）。
2. 在路由器后台开启 **UPnP / NAT-PMP / PCP**（部分固件默认关闭，或称为「NAT 端口映射协议」）。
3. Linux 防火墙放行 UDP 1900（SSDP）与 UDP 5351（NAT-PMP/PCP）。
4. 使用 `natpilot gateway discover` 查看探测日志（`--log-level debug`）。

**Q：Docker 里能映射宿主机端口吗？**
不能。容器位于 Docker 网桥内，只能为自己映射；请使用 `network_mode: host`，或把二进制直接跑在宿主机。

**Q：本机扫描为空？**
本机扫描读取 `/proc/net/{tcp,tcp6,udp,udp6}` 并枚举进程；非 Linux 平台会退化为回环探测。若指定了 `--ports` 作为过滤条件而无匹配，会明确提示。

**Q：规则显示「失败 / 无可用网关」？**
表示规则已入库但未能下发；检查网关可用性、路由器是否允许该端口、以及是否为演练模式（`mapper.dry_run`）。失败原因在列表中可见。

**Q：Electron 版提示「无法启动 NatPilot 服务」？**
说明它没找到或无法执行内置的服务端。安装包会把服务端放在 `resources/bin/` 下；
若是解压出来的免安装目录，请确认整个目录（含 `resources`、`.dll`）被完整拷贝，不要只拷 `NatPilot.exe`。

**Q：忘记管理员密码？**
```bash
natpilot user passwd admin            # 随机生成并打印
natpilot user passwd admin --password '新密码'
```

**Q：如何只做验证不下发？**
在「系统设置」开启 **演练模式**（`mapper.dry_run: true`），或配置文件设置 `mapper.dry_run: true`。

---

## 安全提示

- 开启 `auth.enabled` 后，桌面模式的免登录仅对本机回环生效；对外暴露必须开启鉴权。
- 密码以 bcrypt 存储；会话 Cookie 为 `HttpOnly`；HTTPS 下自动 `Secure`。
- 登录失败次数超限会临时锁定（默认 8 次 / 15 分钟）。
- `NATPILOT_SESSION_SECRET` 建议显式设置并在多实例间保持一致；否则首次启动生成并存于数据目录。
- `export` / `import` 仅管理员可用，导出内容包含规则与标签，请妥善保管。

---

## License

MIT
