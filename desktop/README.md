# NatPilot 桌面版（Electron）

窗口 + 系统托盘的桌面版，**三平台同一套实现**（Windows / macOS / Linux）。

## 行为

| 动作 | 结果 |
| --- | --- |
| 关闭窗口 | 只隐藏窗口，服务继续运行（图标留在托盘 / macOS 菜单栏） |
| 双击托盘图标（Windows/Linux）或点菜单栏图标（macOS） | 重新打开窗口 |
| 托盘菜单 →「打开控制台」 | 重新打开窗口 |
| 托盘菜单 →「在浏览器中打开」 | 用系统浏览器打开当前控制台 URL |
| 托盘菜单 →「退出 NatPilot」（macOS 亦可用 ⌘Q） | **真正退出**：停止后台 `natpilot` 服务并释放端口 |
| 再次启动本程序 | 不会开第二份；已有实例的窗口会被唤到前台 |

窗口大小/位置会记住（`userData/window-state.json`）。控制台只加载本地 URL，其它链接一律交给系统浏览器。

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
Electron 自带这三种平台的托盘实现，因此桌面版统一走 Electron；Go 侧只保留
`natpilot-desktop` 这个**轻量启动器**（无托盘、前台运行、Ctrl+C 退出），供不想装 Electron 的场景使用。
