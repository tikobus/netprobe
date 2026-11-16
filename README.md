# NetProbe — 网络设备探活管理工具

用 Golang 编写的轻量级网络设备探活（健康检查）工具：**单二进制、无外部依赖**，内置 Web 报告界面，支持 systemctl 一键部署。

## 功能特性

- **设备管理**：主机 / HTTP 服务 / TCP 端口 / DNS 等常用类型，REST API + Web 界面增删改查
- **多种探活**：
  | 方式 | 说明 |
  |---|---|
  | `ping` | ICMP 探测（raw → 无特权 UDP → TCP ping 自动降级） |
  | `curl` | HTTP(S) GET：状态码 / 响应关键字 / TLS 证书 / DNS·连接·TLS·首字节分阶段耗时 |
  | `tcp` | TCP 端口连通性 |
  | `dns` | 域名解析（可校验期望 IP） |
- **调度引擎**：每设备独立周期 + 超时 + 失败重试；协程池并发；新设备即时生效；状态变化（up↔down）自动标记
- **日志双写**：SQLite（报表查询）+ 按天滚动的 JSON Lines 文件（审计），过期自动清理
- **Web 报告**：总览仪表盘（在线率/可用率）、设备详情（延迟趋势图/历史记录）、日志检索
- **部署**：提供 systemd unit 与安装脚本，支持 `CAP_NET_RAW` 最小权限运行 ICMP

## 快速开始

```bash
# 本地运行（默认 http://localhost:8080）
make run

# 或直接
go build -o bin/netprobe ./cmd/netprobe
./bin/netprobe -config configs/config.example.yaml
```

打开浏览器访问 `http://127.0.0.1:8080`，点击「+ 添加设备」：

```jsonc
// 主机 ping
{"name": "网关", "type": "host", "target": "192.168.1.1", "interval": 30}

// HTTP 服务（校验状态码与关键字）
{"name": "官网", "type": "http", "target": "https://example.com/health",
 "expect": "{\"status_codes\":[200],\"body_contains\":\"ok\"}"}

// TCP 端口
{"name": "MySQL", "type": "tcp", "target": "db.internal:3306", "interval": 10}

// DNS（校验解析 IP）
{"name": "CoreDNS", "type": "dns", "target": "example.com",
 "expect": "{\"ips\":[\"1.2.3.4\"]}"}
```

## 配置说明（configs/config.example.yaml）

| 项 | 默认 | 说明 |
|---|---|---|
| `server.addr` | `:8080` | Web 监听地址（环境变量 `NETPROBE_ADDR` 覆盖） |
| `server.basic_auth.*` | 关闭 | Web/API Basic Auth |
| `storage.db_path` | `data/netprobe.db` | SQLite 路径（`NETPROBE_DB_PATH`） |
| `storage.db_retain_days` | `7` | 原始探测记录保留天数（0=不清理） |
| `log.dir` | `logs` | 探测日志文件目录（`NETPROBE_LOG_DIR`） |
| `log.retain_days` | `30` | 日志文件保留天数 |
| `prober.concurrency` | `32` | 并发探测协程数 |
| `prober.default_*` | 30/5/2 | 新设备默认周期/超时/重试 |

## REST API

```
GET    /healthz                          存活检查
GET    /api/devices                      设备列表
POST   /api/devices                      创建设备
GET    /api/devices/{id}                 设备详情
PUT    /api/devices/{id}                 更新设备
DELETE /api/devices/{id}                 删除设备（含历史）
POST   /api/devices/{id}/test            立即探测一次
GET    /api/status                       全部设备 + 最近探测结果
GET    /api/report/overview?hours=24     总览报表（可用率统计）
GET    /api/logs?device_id=&result=&from=&to=&limit=&offset=   日志查询
```

## systemd 部署（Linux）

```bash
make build                     # 交叉编译: CGO_ENABLED=0 GOOS=linux GOARCH=amd64 make build
make package                   # 产出 dist-<date>.tar.gz
# 上传解压后
sudo ./install.sh
```

安装脚本完成：拷贝二进制 → 创建 `netprobe` 系统用户 → 写入 `/etc/netprobe/config.yaml`（数据/日志固定在 `/var/lib/netprobe`、`/var/log/netprobe`）→ 注册并启动 systemd 服务（授予 `CAP_NET_RAW` 供 ICMP 使用，其余最小权限加固）。

```bash
systemctl status netprobe      # 运行状态
journalctl -u netprobe -f      # 运行日志（探测失败/状态变化）
```

## Docker 部署

### 直接运行

```bash
docker run -d --name netprobe \
  -p 8080:8080 \
  -v netprobe-data:/var/lib/netprobe \
  -v netprobe-logs:/var/log/netprobe \
  ghcr.io/<OWNER>/netprobe:latest
```

镜像内置默认配置（数据写入 `/var/lib/netprobe/netprobe.db`，日志写入 `/var/log/netprobe`，均已声明为卷）。自定义配置时挂载覆盖：`-v $PWD/config.yaml:/etc/netprobe/config.yaml:ro`。

- 镜像内置 `HEALTHCHECK`（`/healthz` 免鉴权），直接被 Docker/K8s 探活
- 默认以 root 运行以支持原生 ICMP；降权运行：`--user 65534 --cap-add NET_RAW`（此时 raw ICMP 不可用会自动降级 UDP/TCP ping）

### docker compose

```bash
cp docker-compose.yml .  # 修改 image 为你的 ghcr 地址
docker compose up -d
docker compose logs -f
```

### 本地构建镜像

```bash
make docker-build        # 等价 docker build -t netprobe:dev .
```

## CI/CD：GitHub Actions → GitHub Packages (ghcr.io)

仓库已内置 `.github/workflows/docker.yml`，推送到 GitHub 后自动执行：

1. `go test ./...` 全部通过 → 才构建镜像
2. buildx 构建 **linux/amd64 + linux/arm64** 双架构镜像
3. 推送到 `ghcr.io/<OWNER>/<REPO>`，无需手动配置密钥（使用内置 `GITHUB_TOKEN`）

触发与标签规则：

| 触发 | 镜像标签 |
|---|---|
| push 到 `main` | `latest`、`main`、`sha-xxxxxxx` |
| 打 tag `v1.2.3` | `1.2.3`、`1.2`、`sha-xxxxxxx` |
| Pull Request | 仅构建验证，不推送 |
| 手动触发（workflow_dispatch） | 同 push |

首次使用：

1. 推送仓库到 GitHub（`git init && git add . && git commit -m "init" && git remote add origin git@github.com:<OWNER>/netprobe.git && git push -u origin main`）
2. Actions 页面确认 `docker` 工作流执行成功
3. 仓库右侧 **Packages** 出现镜像；或访问 `https://github.com/<OWNER>/?tab=packages`
4. 拉取镜像默认需要登录（包默认私有）：`docker login ghcr.io -u <OWNER>`（密码用 [Personal Access Token](https://github.com/settings/tokens)，需 `read:packages` 权限）
5. 如需匿名拉取：Package 页面 → **Package settings** → **Danger Zone** → **Change visibility** → Public

## 目录结构

```
netprobe/
├── cmd/netprobe/          # 入口：装配 config/store/prober/scheduler/web
├── internal/
│   ├── config/            # YAML 配置 + 默认值 + 环境变量覆盖
│   ├── store/             # SQLite（modernc 纯 Go）：设备 CRUD、探测日志、可用率统计
│   ├── prober/            # ping / curl / tcp / dns 探测器 + 重试逻辑
│   ├── scheduler/         # 周期调度、协程池、状态变化检测
│   ├── recorder/          # 结果双写：DB + 按天滚动日志文件 + 过期清理
│   ├── web/               # REST API + 报表
│   └── webui/             # 内嵌前端（embed，零外部依赖）
├── configs/               # 配置示例
├── deploy/                # systemd unit + install.sh
├── .github/workflows/     # GitHub Actions（测试 + 镜像推送 ghcr.io）
├── Dockerfile             # 多阶段构建（纯 Go，无 cgo）
├── docker-compose.yml     # 容器编排示例
└── DEVELOPMENT_PLAN.md    # 开发计划
```

## 开发

```bash
go test ./...    # 单元测试（探测器/调度/存储/Web）
make build       # 编译
make package     # 打发布包
```

### 探活方式说明（ping 降级链）

1. `ip4:icmp` raw socket —— 需要 root / CAP_NET_RAW，精度最高
2. `udp4` 无特权 ICMP —— macOS / Linux（`net.ipv4.ping_group_range`）可用
3. TCP ping —— 对 80/443/22（可用 `expect.tcp_fallback_ports` 自定义）发起 TCP 连接，任一成功即判定存活

HTTP 探测 `expect` 支持：`status_codes`（默认 200~399）、`body_contains`、`insecure_tls`（自签名证书）。
