# syntax=docker/dockerfile:1
# ---------- 构建阶段 ----------
FROM golang:1.26-alpine AS builder

ARG VERSION=dev

WORKDIR /src

# 先拷依赖清单，利用层缓存
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X netprobe/internal/web.Version=${VERSION}" \
      -o /out/netprobe ./cmd/netprobe

# ---------- 运行阶段 ----------
FROM alpine:3.21

# ca-certificates: HTTPS 探测；tzdata: 时区
RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /out/netprobe /usr/local/bin/netprobe

# 内置默认配置（数据/日志落在卷路径），可被挂载覆盖
COPY configs/config.example.yaml /etc/netprobe/config.yaml
RUN sed -i \
      -e 's#^  db_path:.*#  db_path: /var/lib/netprobe/netprobe.db#' \
      -e 's#^  dir:.*#  dir: /var/log/netprobe#' \
      /etc/netprobe/config.yaml \
    && mkdir -p /var/lib/netprobe /var/log/netprobe

ENV TZ=Asia/Shanghai

EXPOSE 8080
VOLUME ["/var/lib/netprobe", "/var/log/netprobe"]

# /healthz 不需要鉴权，可直接探测
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q --spider http://127.0.0.1:8080/healthz || exit 1

# 以 root 运行以支持原生 ICMP；如需降权运行见 README（user + cap_add NET_RAW）
ENTRYPOINT ["/usr/local/bin/netprobe"]
CMD ["-config", "/etc/netprobe/config.yaml"]
