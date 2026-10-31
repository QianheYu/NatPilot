# NatPilot 桌面版（Electron）

窗口 + 系统托盘的桌面版，**三平台同一套实现**（Windows / macOS / Linux）。

## 行为

| 动作 | 结果 |
| --- | --- |
| 关闭窗口 | 只隐藏窗口，服务继续运行（图标留在托盘 / macOS 菜单栏） |
| 双击托盘图标（Windows/Linux）或点菜单栏图标（macOS） | 重新打开窗口 |
| 托盘菜单 →「打开控制台」 | 重新打开窗口 |
| 托盘菜单 →「系统设置…」 | 打开窗口并直接停在**系统设置**页（运行配置都在这里改） |
| 托盘菜单 →「重启服务」 | 停掉后台 `natpilot` 再拉起一个，并让窗口连到新端口（改监听端口/TLS 之后用它生效） |
| 托盘菜单 →「在浏览器中打开」 | 用系统浏览器打开当前控制台 URL |
| 托盘菜单 →「打开数据目录」 | 在文件管理器里定位数据库所在目录 |
| 托盘菜单 →「开机自启」 | 勾选后随系统登录启动（见下文） |
| 托盘菜单 →「检查更新…」 | 查询最新发布版本；有新版时给出发布页链接 |
| 托盘菜单 →「退出 NatPilot」（macOS 亦可用 ⌘Q） | **真正退出**：停止后台 `natpilot` 服务并释放端口 |
| 再次启动本程序 | 不会开第二份；已有实例的窗口会被唤到前台 |

窗口大小/位置会记住（`userData/window-state.json`）。控制台只加载本地 URL，其它链接一律交给系统浏览器。

托盘菜单第一行会显示当前状态：`NatPilot 运行中 — http://127.0.0.1:<port>`，服务未起来时显示「NatPilot 服务未运行」并禁用相关菜单项。

## 修改运行配置

窗口里就是完整的控制台，**运行配置统一在「系统设置」页修改**（桌面模式自动免登录、以管理员身份进入）：

- 立即生效：端口映射策略、自动发现、扫描参数、事件与日志、用户与安全等。
- 需要重启服务才生效：监听端口 / 监听地址 / TLS 证书等——改完从托盘点一次「重启服务」即可，窗口会自动重连到新地址。

## 开机自启与检查更新

**开机自启**是托盘菜单里的一个勾选项（勾选状态由系统实际注册情况回读，不是本地记的标志位）：

- Windows / macOS：走 Electron 的 `setLoginItemSettings`（Windows 写 `HKCU\...\Run`）。
- Linux：写 `$XDG_CONFIG_HOME/autostart/natpilot.desktop`（缺省 `~/.config/autostart/`），
  `Exec=` 走标准 Desktop Entry 规则，路径含空格会自动加引号；AppImage 场景注册的是
  `$APPIMAGE` 文件本身而不是临时挂载点。
- 注册的命令带 `--autostart`：登录时**不弹窗口**，只在托盘出现并照常拉起后台服务；
  点托盘「打开控制台」或双击图标才开窗。
- 开发模式（`npm start`）下该项显示为「开机自启（仅安装版可用）」并禁用——那时
  `process.execPath` 是 `electron.exe`，注册上去没有意义。

**检查更新**在启动 10 秒后静默查一次，之后也可从托盘手动触发：

- 请求 `https://api.github.com/repos/<UPDATE_REPO>/releases/latest`，与 `app.getVersion()` 做
  数字比较（`0.10.0 > 0.9.0`，不会按字符串比大小）。
- 有新版本时弹出对话框并给出「打开发布页」；无版本发布（HTTP 404）时提示「已是最新版本」，
  不会误报网络错误；网络不可用时静默模式下只记日志，不打扰使用。
- `UPDATE_REPO` 定义在 `electron/main.js` 顶部，fork 后改成自己的仓库即可。

两块逻辑（`electron/version.js`、`electron/autostart.js`）与 Electron 解耦，可单独跑测试：

```bash
npm test      # node --test，19 个用例，覆盖版本比较与三平台自启写入
```

## 退出后不留孤儿进程

桌面壳被正常退出时会先停掉服务端子进程。若它是被强杀的（任务管理器结束任务、崩溃），服务端也能自己退出：
Electron 通过 `NATPILOT_PARENT_PID` 把主进程 pid 传给服务端，服务端每 2 秒检查一次，父进程消失就以
`exit 0` 优雅退出。因此不会残留在后台占着端口的 `natpilot` 进程。

## 开发运行

```bash
# 1) 先在仓库根目录构建服务端二进制（Electron 会去找 ../bin/natpilot）
cd ..
go build -o bin/natpilot .        # Windows: bin\natpilot.exe（不需要 -H windowsgui）

# 2) 启动桌面版
cd desktop
npm install
npm start
```

托盘/应用图标已经生成好了；修改图标后重新生成：

```bash
npm run icons     # node electron/assets/make-icons.js
```

## 打包

`electron-builder` 只为本机平台产出安装包（跨平台打包需要对应平台的工具链/Wine），
并且**必须先把对应平台的服务端二进制放进 `bin/`**：

```bash
# Windows
go build -o bin\natpilot.exe .
cd desktop && npm run dist:win      # release/NatPilot Setup x.y.z.exe

# macOS
go build -o bin/natpilot .
cd desktop && npm run dist:mac      # release/NatPilot-x.y.z.dmg

# Linux
go build -o bin/natpilot .
cd desktop && npm run dist:linux    # release/NatPilot-x.y.z.AppImage / .deb
```

`extraResources` 会把 `../bin/natpilot[.exe]` 复制到安装包的 `resources/bin/`，
运行时 `serverBinary()` 优先从这个位置加载，因此用户机器上不需要额外安装 Go 程序。

### 网络受限时打包（不重新下载 Electron）

`electron-builder` 默认会自己去 GitHub 下载一份 Electron 压缩包。既然 `npm install` 已经把
分发包放在 `node_modules/electron/dist` 了，直接让它复用即可：

```powershell
# 只产免安装目录 release/win-unpacked/（双击 NatPilot.exe 就能用）
npx electron-builder --win --dir `
  --config.electronDist="$PWD\node_modules\electron\dist" `
  --config.win.signAndEditExecutable=false
```

`signAndEditExecutable=false` 会跳过需要额外下载的 `winCodeSign`（不重写 exe 的图标与版本信息，
功能不受影响）。要出 NSIS 安装包时把 `--dir` 去掉，并保证能访问 GitHub 或已配好镜像。

`release/win-unpacked/` 是完整可用的免安装版：

```
release/win-unpacked/
├─ NatPilot.exe                      ← 双击运行
└─ resources/
   ├─ app.asar                       ← 桌面壳（main.js + 托盘图标）
   └─ bin/natpilot.exe               ← 内置服务端
```

也可从仓库根目录一键完成「构建 Go 二进制 + 打包」：

```bash
make desktop            # macOS / Linux
.\build.ps1 -Electron   # Windows
```

## 图标

`electron/assets/make-icons.js` 用纯 Node（zlib）画 PNG，不依赖 canvas / Python：

| 文件 | 用途 |
| --- | --- |
| `assets/tray.png` / `tray@2x.png` | Windows、Linux 托盘图标（彩色） |
| `assets/trayTemplate.png` / `@2x` | macOS 菜单栏模板图（单色 + alpha，自动适配深色/浅色） |
| `build/icon.png`（512×512） | 应用图标，`electron-builder` 由此生成 `.ico` / `.icns` |

## 为什么桌面版用 Electron

macOS 的 `NSStatusItem` 需要 Objective-C 运行时，Linux 的系统托盘需要 StatusNotifierItem（D-Bus）或 GTK——
两条路都绕不开 cgo 或自研协议栈，会破坏「`CGO_ENABLED=0` + 任意机器交叉编译」的约束。
Electron 自带这三种平台的托盘实现，因此桌面版统一走 Electron。
Go 侧不再发布单独的启动器：需要窗口与托盘就用 Electron 版，服务器环境直接
`natpilot serve` + 浏览器。
## 启动器设置入口

服务运行时：主窗口内 **系统 → 启动器设置**（`#/launcher`），与其它页面同一外壳。

服务未运行时：自动弹出独立启动面板（此时 Web 控制台由服务端提供，服务没起来就打不开），
面板里可修改配置并点击「启动服务」。

该页面只在桌面版存在：浏览器访问时侧边栏不显示入口，直接访问提示"仅桌面版可用"。

## 启动器设置（launcher.json）

桌面壳自身的配置与 Web 控制台的 `系统设置` **分开**，存放在
`<userData>/launcher.json`（Windows 为 `%APPDATA%\natpilot-desktop\launcher.json`）。
即使服务无法启动（路径写错、端口被占用），也可以从托盘打开
「启动器设置…」修正。

| 键 | 默认 | 说明 |
| --- | --- | --- |
| `autoStartService` | `true` | 启动器启动时是否自动拉起 Go 服务；`false` 时只显示启动面板 |
| `serverBinary` | `""` | 服务可执行文件路径，空=自动探测 |
| `dataDir` | `""` | 数据目录，空=`<userData>/natpilot` |
| `host` | `"127.0.0.1"` | 服务监听地址（默认只绑回环） |
| `port` | `0` | 固定端口；`0`=系统分配（失败自动换端口重试） |
| `extraArgs` | `[]` | 追加给 `serve` 的参数，逐项一个 token |
| `closeToTray` | `true` | 关闭窗口时驻留托盘；`false` 则关闭即退出 |
| `startHiddenOnAutostart` | `true` | 开机自启时只驻留托盘 |
| `checkUpdatesOnStart` | `true` | 启动后静默检查一次更新 |

最终命令行形如：

```
natpilot.exe --host <host> --port <port> --mode desktop --data-dir <dir> <extraArgs...> serve
```