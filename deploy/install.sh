#!/usr/bin/env bash
# NetProbe 安装脚本（Linux + systemd）
# 用法: sudo ./install.sh
set -euo pipefail

BINARY=netprobe
INSTALL_BIN=/usr/local/bin/netprobe
CONF_DIR=/etc/netprobe
DATA_DIR=/var/lib/netprobe
LOG_DIR=/var/log/netprobe
SERVICE_FILE=/etc/systemd/system/netprobe.service

[[ $EUID -eq 0 ]] || { echo "请用 root 运行: sudo $0"; exit 1; }
command -v systemctl >/dev/null || { echo "未找到 systemctl（仅支持 systemd 系统）"; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SRC_BIN="$SCRIPT_DIR/../bin/$BINARY"
[[ -f "$SRC_BIN" ]] || { echo "未找到 $SRC_BIN，请先执行 make build"; exit 1; }

echo "==> 安装二进制 $INSTALL_BIN"
install -m 0755 "$SRC_BIN" "$INSTALL_BIN"

echo "==> 创建运行用户/目录"
id netprobe &>/dev/null || useradd --system --no-create-home --shell /usr/sbin/nologin netprobe
mkdir -p "$CONF_DIR" "$DATA_DIR" "$LOG_DIR"

echo "==> 写入配置 $CONF_DIR/config.yaml"
if [[ -f "$CONF_DIR/config.yaml" ]]; then
    echo "    配置已存在，跳过（如需重置请手动删除）"
else
    sed -e "s#^  db_path:.*#  db_path: $DATA_DIR/netprobe.db#" \
        -e "s#^  dir:.*#  dir: $LOG_DIR#" \
        "$SCRIPT_DIR/../configs/config.example.yaml" > "$CONF_DIR/config.yaml"
fi
chown -R netprobe:netprobe "$DATA_DIR" "$LOG_DIR"
chown root:netprobe "$CONF_DIR/config.yaml"
chmod 640 "$CONF_DIR/config.yaml"

echo "==> 安装 systemd 服务"
install -m 0644 "$SCRIPT_DIR/netprobe.service" "$SERVICE_FILE"
systemctl daemon-reload

echo "==> 启动服务"
systemctl enable --now netprobe
sleep 1
systemctl --no-pager --lines 5 status netprobe || true

echo
echo "安装完成："
echo "  配置文件: $CONF_DIR/config.yaml"
echo "  数据目录: $DATA_DIR"
echo "  日志目录: $LOG_DIR （按天滚动的探测日志）"
echo "  Web 界面: http://<本机IP>:8080/"
echo "常用命令:"
echo "  systemctl status netprobe    查看状态"
echo "  systemctl restart netprobe   重启（改配置后）"
echo "  journalctl -u netprobe -f    跟踪运行日志"
