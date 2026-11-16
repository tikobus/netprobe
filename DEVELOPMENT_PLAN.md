# NetProbe —— 网络设备探活管理工具 开发计划

> 版本：v0.1（计划稿）
> 目标：用 Golang 开发一个轻量级网络设备管理 / 探活（健康检查）工具

---

## 1. 项目概述

一个单体二进制的网络设备探活管理工具，核心能力：

- **设备管理**：维护被监控对象清单（主机、HTTP 服务、TCP 端口、DNS 等）
- **多方式探活**：ICMP Ping、HTTP(S) curl、TCP 端口、DNS 解析等
- **调度引擎**：按设备配置的周期并发探测，支持超时与重试
- **日志落盘**：每次探测结果写入日志文件（按天滚动）
- **Web 报告**：内置 Web UI 查看设备状态总览、历史可用率、探测日志
- **systemd 部署**：提供 unit 文件，支持 systemctl 一键部署

---

## 2. 需求分析

### 2.1 功能需求

| 模块 | 需求 |
|---|---|
| 设备管理 | 增删改查设备；字段：名称、类型、地址、探活方式、周期、超时、重试、标签、启用开关 |
| 设备类型 | host（主机）、http（HTTP 服务）、tcp（TCP 端口）、dns（DNS 服务），预留自定义扩展 |
| 探活方式 | ping（ICMP）、curl（HTTP GET，判断状态码/响应体关键字/耗时）、tcp（端口连通）、dns（解析结果） |
| 调度 | 每设备独立周期调度；并发探测；失败重试；抖动避免同时打爆 |
| 日志 | 每次探测一条记录（时间、目标、方式、结果、耗时、错误信息），按天滚动文件，保留 N 天自动清理 |
| 报告 | Web 页面：总览仪表盘（在线/离线数、可用率）、设备详情、历史趋势、原始日志查看 |
| 告警（可选二期） | 离线超过阈值时 webhook 通知 |
| 部署 | 提供 systemd unit；支持配置文件 + 环境变量；开机自启 |

### 2.2 非功能需求

- 单二进制，无外部依赖（不依赖 MySQL，使用 SQLite 或纯文件存储）
- 低资源占用：目标内存 < 100MB，可监控 1000+ 设备
- 探测并发可控，不阻塞主循环
- Web 端口可配置，可加 Basic Auth

---

## 3. 技术选型

| 项 | 选择 | 理由 |
|---|---|---|
| 语言 | Go 1.22+ | 标准库强、并发模型适合探测场景 |
| Web 框架 | `net/http` + `chi`（或标准库） | 轻量、路由清晰 |
| ICMP Ping | `problame/go-mpping` 或自实现 raw socket；非 root 场景用 TCP Ping（`telnet` 方式 80/443 探测） | raw ICMP 需 root/CAP_NET_RAW，需提供降级 |
| HTTP 探测 | `net/http` + `httptrace` | 原生，拿 DNS/连接/TLS/总耗时 |
| 存储 | SQLite（`modernc.org/sqlite` 纯 Go，无 cgo） | 报表需要历史数据；避免外部依赖 |
| 日志 | `slog` 结构化 + `lumberjack` 滚动 | 标准库 + 成熟滚动方案 |
| 配置 | YAML（`gopkg.in/yaml.v3`） | 人肉可读可改 |
| 前端 | 内嵌单页（原生 HTML/JS + Chart.js），`embed` 打进二进制 | 单二进制交付，免 Node 构建链 |
| 调度 | 自研时间轮/简单 `time.Ticker` per 设备 + 协程池 | 需求简单，不引入 cron 库（或用 `robfig/cron`） |

---

## 4. 系统架构

```
┌─────────────────────────────────────────────┐
│                  netprobe                    │
│                                             │
│  config.yaml ──► Config Loader              │
│                     │                       │
│              ▼                              │
│  ┌──────────────┐    ┌───────────────────┐  │
│  │ Device Store │◄──►│  Scheduler        │  │
│  │  (SQLite)    │    │  (per-dev ticker) │  │
│  └──────▲───────┘    └────────┬──────────┘  │
│         │                     │ 触发        │
│         │            ┌────────▼──────────┐  │
│  ┌──────┴───────┐    │  Prober Pool      │  │
│  │ REST API     │    │  ┌─────────────┐  │  │
│  │ /api/devices │    │  │ ping prober │  │  │
│  │ /api/status  │    │  │ http prober │  │  │
│  │ /api/logs    │    │  │ tcp prober  │  │  │
│  │ /api/report  │    │  │ dns prober  │  │  │
│  └──────┬───────┘    │  └─────────────┘  │  │
│         │            └────────┬──────────┘  │
│  ┌──────▼───────┐             │ 结果        │
│  │ Web UI       │    ┌────────▼──────────┐  │
│  │ (embed)      │    │ Recorder          │  │
│  └──────────────┘    │ ├► SQLite 历史    │  │
│                      │ └► 日志文件(滚动)  │  │
│                      └───────────────────┘  │
└─────────────────────────────────────────────┘
```

### 模块职责

1. **config**：加载/校验/热重载配置
2. **store**：设备 CRUD、探测历史、可用率统计
3. **scheduler**：为每个启用设备维护调度周期，投递探测任务到协程池
4. **prober**：探测器接口 + 各实现
5. **recorder**：双写 SQLite（供报表）与日志文件（供审计）
6. **web**：REST API + 静态页面
7. **deploy**：systemd unit、安装脚本

---

## 5. 核心设计

### 5.1 数据模型（SQLite）

```sql
-- 设备表
CREATE TABLE device (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT NOT NULL,          -- 展示名
  type        TEXT NOT NULL,          -- host / http / tcp / dns
  method      TEXT NOT NULL,          -- ping / curl / tcp / dns
  target      TEXT NOT NULL,          -- IP / URL / host:port
  interval    INTEGER DEFAULT 30,     -- 探测周期(秒)
  timeout     INTEGER DEFAULT 5,      -- 超时(秒)
  retries     INTEGER DEFAULT 2,      -- 失败重试次数
  expect      TEXT,                   -- 期望：状态码/关键字，JSON
  enabled     INTEGER DEFAULT 1,
  tags        TEXT,                   -- 逗号分隔标签
  created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 探测结果表（历史，按天分区/定期归档）
CREATE TABLE probe_log (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  device_id  INTEGER NOT NULL,
  success    INTEGER NOT NULL,
  latency_ms REAL,
  detail     TEXT,                    -- 错误信息 / HTTP 状态码等
  probed_at  DATETIME NOT NULL
);
CREATE INDEX idx_probe_log_dev_time ON probe_log(device_id, probed_at);
```

### 5.2 探测器接口

```go
type Result struct {
    Success   bool
    Latency   time.Duration
    Detail    string          // 失败原因或附加信息
    Extra     map[string]any  // http: status_code, tls, dns_time ...
}

type Prober interface {
    Probe(ctx context.Context, d Device, expect Expect) Result
}
```

- **ping**：优先 ICMP（需权限）；无权限自动降级 TCP Ping（对目标 80/443/22 发 SYN）
- **curl**：`GET` 目标 URL，校验状态码（默认 2xx/3xx）、可选响应体关键字、证书有效期；记录各阶段耗时
- **tcp**：`net.DialTimeout("tcp", addr)` 判连通
- **dns**：解析域名，比对期望 IP / 解析耗时

### 5.3 调度与并发

- 每设备一个调度条目（下次执行时间），由单个调度协程扫描到期任务
- 探测任务进入带缓冲 channel，worker pool（默认 32 并发）消费
- 结果经重试逻辑后写入 recorder；状态变化（up↔down）打特殊事件日志

### 5.4 日志与报告

- 文件日志：`logs/probe-YYYYMMDD.log`，JSON Lines 格式，lumberjack 按天切割，默认保留 30 天
- Web 报告：
  - **总览**：设备在线率饼图、最近 24h 可用率排行、down 设备列表
  - **详情**：单设备延迟折线（24h/7d）、最近 100 条探测记录、up/down 时间线
  - **日志**：按设备/时间/结果过滤原始日志

### 5.5 REST API（草案）

```
GET/POST/PUT/DELETE  /api/devices           设备管理
POST                 /api/devices/{id}/test 立即探测一次
GET                  /api/status             全部设备当前状态
GET                  /api/report/overview    总览统计
GET                  /api/report/device/{id} 单设备历史/趋势
GET                  /api/logs?device=&from=&to=&result=  日志查询
```

---

## 6. 目录结构

```
netprobe/
├── cmd/netprobe/main.go        # 入口
├── internal/
│   ├── config/                 # 配置加载
│   ├── store/                  # SQLite 访问层
│   ├── prober/                 # ping/http/tcp/dns 探测器
│   ├── scheduler/              # 调度与协程池
│   ├── recorder/               # 结果落库+日志文件
│   ├── web/                    # API handler
│   └── webui/                  # embed 前端静态文件
│       └── static/index.html ...
├── deploy/
│   ├── netprobe.service        # systemd unit
│   └── install.sh              # 安装脚本
├── configs/config.example.yaml
├── Makefile
├── go.mod
└── DEVELOPMENT_PLAN.md
```

---

## 7. 部署方案（systemd）

`deploy/netprobe.service`：

```ini
[Unit]
Description=NetProbe Network Device Monitor
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=netprobe
ExecStart=/usr/local/bin/netprobe -config /etc/netprobe/config.yaml
Restart=on-failure
RestartSec=5
# ICMP 探测所需能力
AmbientCapabilities=CAP_NET_RAW
CapabilityBoundingSet=CAP_NET_RAW
# 加固
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/var/lib/netprobe /var/log/netprobe

[Install]
WantedBy=multi-user.target
```

安装：`install.sh` 完成拷贝二进制、建用户、写配置、`systemctl enable --now netprobe`。

---

## 8. 开发里程碑

| 阶段 | 内容 | 产出 | 预估 |
|---|---|---|---|
| **M1 骨架** | 项目初始化、配置加载、日志框架、SQLite store、设备 CRUD API | 可编译运行，`/api/devices` 可用 | 2~3 天 |
| **M2 探活核心** | prober 接口 + tcp/http 实现（curl）、调度器、worker pool、结果落库与文件日志 | `netprobe test` 探测可用，周期任务跑通 | 3~4 天 |
| **M3 ping/dns + 降级** | ICMP 探测、无权限 TCP ping 降级、dns 探测、重试策略 | 四种探活方式齐备 | 2~3 天 |
| **M4 Web 报告** | 总览/详情/日志页面、趋势图、API 报表接口、Basic Auth | 浏览器可看报告 | 3~4 天 |
| **M5 部署交付** | systemd unit、install.sh、Makefile（build/tar/package）、配置样例与 README | 一条命令部署到 Linux | 1~2 天 |
| **M6 加固（可选）** | 告警 webhook、配置热重载、数据归档清理、压测 1000 设备 | 生产可用 | 3~5 天 |

总计约 **3 周**（单人，M6 可延后）。

---

## 9. 风险与应对

| 风险 | 应对 |
|---|---|
| raw ICMP 需 root | unit 里授 CAP_NET_RAW；运行时探测失败自动降级 TCP ping |
| SQLite 历史膨胀 | probe_log 定期清理/按天聚合归档（原始保留 7 天，聚合保留 1 年） |
| 大量设备同时探测 | 协程池限并发 + 调度加随机抖动 |
| 单点故障 | 二期支持导出 Prometheus 指标，接入外部告警 |

---

## 开发进度（2026-11-16 更新）

- [x] **M1 项目骨架**：配置加载（YAML+默认值+环境变量覆盖）、SQLite 存储、设备 CRUD REST API、Basic Auth、健康检查
- [x] **M2 探活引擎**：Prober 接口 + Registry、curl（分阶段计时）/tcp 探测器、失败重试、调度器（协程池+随机抖动+状态变化检测）、结果双写（SQLite + 按天滚动 JSON Lines 日志）+ 过期清理
- [x] **M3 ping/dns**：ICMP 三级降级（raw→UDP 无特权→TCP ping）、DNS 期望 IP 校验
- [x] **M4 Web 报告**：内嵌单页 UI（总览仪表盘/设备详情延迟趋势图 SVG/日志检索分页/设备表单弹窗）、`/api/report/overview` 可用率报表
- [x] **M5 部署交付**：systemd unit（CAP_NET_RAW+安全加固）、install.sh 一键安装、Makefile package 打包、README
- [ ] M6（可选）：告警 webhook、配置热重载、1000 设备压测

全部单元测试通过；端到端冒烟验证：四类探测器、周期调度、立删改即时生效（Kick 重载）、优雅停机（停机中的取消探测不再误记失败）。
- [x] **CI/CD**：Dockerfile（多阶段、多架构、HEALTHCHECK、/healthz 免鉴权）+ GitHub Actions（测试→buildx 构建 amd64/arm64→推送 ghcr.io）+ docker-compose 示例
