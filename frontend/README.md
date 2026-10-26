# NatPilot 前端（独立子项目）

这是 NatPilot 的 Web 控制台，**一个独立的 Vue 3 + Vite 项目**，只是恰好放在 Go 仓库的
`frontend/` 子目录里。它有自己的 `package.json` / 依赖 / 构建产物，与后端通过 HTTP API
（`/api/*`）和 SSE（`/api/logs/stream`）通信，不共享任何 Go 代码。

构建产物会写入 `../internal/webui/dist/`，由 Go 的 `//go:embed all:dist` 打进单一二进制，
所以发布时只需要一个 `natpilot.exe`，前端不需要单独部署。

## 快速开始

```bash
cd frontend
npm install
npm run dev      # http://127.0.0.1:5173，/api 反向代理到 127.0.0.1:8787
```

构建（产物落到 `internal/webui/dist/`，随后重新 `go build` 即生效）：

```bash
npm run build
# 或回到仓库根目录
make frontend        # 构建前端 + 编译 Go 二进制
```

> 构建后 `internal/webui/dist/placeholder.html` 会被 Vite 的 `emptyOutDir` 清掉，请从
> Git 恢复（`git checkout -- internal/webui/dist/placeholder.html`）。它是全新克隆时
> `go:embed` 的兜底文件，不能丢。

## 目录结构

```
frontend/
├── index.html              入口 HTML
├── vite.config.js          base/base './'、outDir 指向 Go embed 目录、dev 代理
├── package.json            独立依赖清单
└── src/
    ├── nav.js              ★ 导航唯一数据源：模块分组、页面、图标、描述
    ├── router.js           路由表（由 nav.js 派生）
    ├── App.vue             外壳编排：启动引导、实时事件、徽章、布局
    ├── layout/
    │   ├── AppSidebar.vue  可折叠侧边栏（按模块分组 + 待处理徽章）
    │   ├── AppTopbar.vue   顶栏：面包屑、全局搜索、通知、监控状态、主题、用户菜单
    │   └── CommandPalette.vue  Ctrl+K 命令面板（页面搜索与跳转）
    ├── layout.css          控制台外壳样式（.app-* / .sb-* / .tb-* / .cp-*）
    ├── style.css           设计令牌（暗/亮主题）与通用组件样式
    ├── api.js              统一 API 客户端（401 处理、错误提示）
    ├── store.js            轻量响应式状态（身份、主题、toast）
    ├── composables/useLive.js  SSE 订阅与自动重连
    ├── components/         Badge / Modal / Switch / TagInput / EmptyState / ChangePassword
    ├── utils/format.js     时间、状态、端口等格式化
    └── views/              12 个功能页面
```

## 界面结构

左侧固定侧边栏 + 顶部固定顶栏 + 可滚动内容区（企业控制台经典三段式）：

| 模块 | 页面 |
| --- | --- |
| 概览 | 运行总览 |
| 端口映射 | 映射规则、端口发现 |
| 网络资源 | 网关与协议、主机清单 |
| 自动发现 | 扫描任务、策略与监控、计划任务 |
| 安全与审计 | 用户与权限（仅管理员）、日志与审计 |
| 系统 | 系统设置（仅管理员）、API 浏览器（仅管理员） |

顶栏提供：侧边栏折叠、面包屑（模块 / 页面）、全局搜索（`Ctrl/Cmd + K`）、通知铃
（未读事件，可直接标为已读）、后台监控状态、实时通道状态、主题切换、用户菜单
（修改密码 / 日志审计 / 退出登录）。窄屏（≤860px）下侧边栏自动变为抽屉。

## 新增一个页面

只需要改两处：

1. 在 `src/views/` 新建组件。
2. 在 `src/nav.js` 对应模块的 `items` 里加一行 `page(...)`。

路由、侧边栏、面包屑、命令面板会自动同步——它们都从 `nav.js` 读取，不存在
"加了页面但菜单里看不到"的情况。仅管理员可见的页面加 `{ admin: true }`。

## 主题

`style.css` 顶部的 CSS 变量同时定义了暗色（`:root`）和亮色（`[data-theme='light']`）
两套令牌；切换只在 `<html>` 上改 `data-theme`，并在 `localStorage` 记住选择。

## 表格树形分组（端口发现 / 映射规则）

两页的表格都是三层树，两级可折叠，行内还可展开详情：

```
▾ 192.168.1.0/24 · 2 台主机 · 7 个端口      ← 一级：网段（可收起）
   ▾ 04:68:74:d9:bf:5a · 192.168.1.10 · 5  ← 二级：主机（可收起，标题含 MAC 与当前 IP）
        ▸ 8080  tcp  ...                     ← 三级：端口行（点箭头展开详情）
```

层级键的选择：

- **一级网段**：IPv4 取 `/24`；`127.x` 归「本机回环」；IPv6/主机名归「其它主机」。
- **二级主机**：优先 MAC，解析不到退化为 IP。规则与端口记录里只存 IP，而 IP 会随 DHCP
  变化；`hosts` 表以 MAC 为身份，端口↔策略绑定也走 MAC。因此能从 `/api/hosts` 解析出该 IP
  当前所属主机的 MAC 时就用 `mac:<mac>` 作键 —— 设备换地址后仍留在同一台主机下，标题里的
  「当前 IP」跟着更新；解析不到（远程主机、未发现、已清理）则退化为 `ip:<ip>`，否则这些行
  会从界面上消失。
- **详情**：端口页展示进程/PID/服务/延迟/来源规则/指纹；规则页展示目标、外部映射、网关、
  来源、更新时间和最近错误。

实现：`src/composables/useHostGroups.js`（`groupBySubnet` / `rowsForTree` / `rowKey`，
以及两级折叠集合的 localStorage 持久化）；表格在 `<tbody>` 内插入 `tr.subnet-row`、
`tr.host-row`、`tr.detail-row`；样式在 `responsive.css` §11，移动端卡片化时前两者渲染为
卡片标题条。