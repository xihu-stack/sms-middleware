#!/usr/bin/env bash
# 短信中间件一键安装（自动从 GitHub Release 下载预编译二进制，无需装 Go）
#
# 用法（任选其一）：
#   curl -fsSL https://raw.githubusercontent.com/xihu-stack/sms-middleware/main/install.sh | sudo bash
#   sudo bash install.sh            # 当前目录有 sms-middleware 二进制时，优先用本地的（离线可用）
#
# 幂等：可重复运行，每次用新输入更新配置并重启服务。

set -euo pipefail

REPO="xihu-stack/sms-middleware"
INSTALL_DIR=/opt/sms-middleware
PORT=8080

[ "$(id -u)" -eq 0 ] || { echo "请用 sudo 运行：sudo bash install.sh"; exit 1; }

# 1) 取二进制：本地有就用本地，否则按架构从最新 Release 下载
BIN="$PWD/sms-middleware"
DOWNLOADED=0
if [ ! -f "$BIN" ]; then
  case "$(uname -m)" in
    x86_64)         ARCH=amd64 ;;
    aarch64|arm64)  ARCH=arm64 ;;
    *) echo "不支持的架构: $(uname -m)（可手动编译后把 sms-middleware 放到当前目录重试）"; exit 1 ;;
  esac
  URL="https://github.com/$REPO/releases/latest/download/sms-middleware-linux-$ARCH"
  echo "下载二进制: $URL"
  TMP="$(mktemp)"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$URL" -o "$TMP"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$TMP" "$URL"
  else
    echo "需要 curl 或 wget 来下载二进制"; rm -f "$TMP"; exit 1
  fi
  BIN="$TMP"; DOWNLOADED=1
fi

# 2) 收集配置（仅 5 项必填；其余用默认值，可之后改 config.env）
old() { { [ -f "$INSTALL_DIR/config.env" ] && grep -m1 -E "^$1=" "$INSTALL_DIR/config.env" | cut -d= -f2-; } || true; }
ask()  { local p="$1" d="${2-}" v; read -rp "$p${d:+ [$d]}: " v || v=""; ANS="${v:-$d}"; }
asks() { local p="$1" d="${2-}" v; read -rsp "$p${d:+ (回车保留)}: " v; echo; ANS="${v:-$d}"; }

echo "=== 阿里云短信配置（回车保留已存值）==="
ask  "AccessKey ID"             "$(old ALIYUN_ACCESS_KEY_ID)";     AK="$ANS"
asks "AccessKey Secret"         "$(old ALIYUN_ACCESS_KEY_SECRET)"; SK="$ANS"
ask  "签名 SignName"            "$(old ALIYUN_SIGN_NAME)";         SIGN="$ANS"
ask  "模板 TemplateCode"        "$(old ALIYUN_TEMPLATE_CODE)";     TPL="$ANS"
ask  "IP白名单(CIDR/IP,逗号分隔)" "$(old IP_ALLOWLIST)";             IP="$ANS"

if [ -z "$AK" ] || [ -z "$SK" ] || [ -z "$SIGN" ] || [ -z "$TPL" ]; then
  echo "AccessKey / 签名 / 模板 不能为空，已中止。" >&2; exit 1
fi

# 3) 落盘二进制 + 配置
mkdir -p "$INSTALL_DIR"
install -m 0755 "$BIN" "$INSTALL_DIR/sms-middleware"
[ "$DOWNLOADED" = 1 ] && rm -f "$BIN"

cat > "$INSTALL_DIR/config.env" <<EOF
LISTEN=:$PORT
IP_ALLOWLIST=$IP
ALIYUN_REGION=cn-hangzhou
ALIYUN_ACCESS_KEY_ID=$AK
ALIYUN_ACCESS_KEY_SECRET=$SK
ALIYUN_SIGN_NAME=$SIGN
ALIYUN_TEMPLATE_CODE=$TPL
ALIYUN_TEMPLATE_PARAM_KEY=content
ALIYUN_TIMEOUT=5s
ALIYUN_ENDPOINT=https://dysmsapi.aliyuncs.com
EOF
chmod 600 "$INSTALL_DIR/config.env"
echo "已写入 $INSTALL_DIR/config.env"

# 4) 安装 systemd 服务
cat > /etc/systemd/system/sms-middleware.service <<EOF
[Unit]
Description=SMS Middleware (Aliyun SMS relay)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
EnvironmentFile=$INSTALL_DIR/config.env
ExecStart=$INSTALL_DIR/sms-middleware
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=$INSTALL_DIR

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now sms-middleware
sleep 1

# 5) 放行端口（尽力而为，失败不中断）
if command -v firewall-cmd >/dev/null 2>&1; then
  firewall-cmd --add-port=$PORT/tcp --permanent >/dev/null 2>&1 || true
  firewall-cmd --reload >/dev/null 2>&1 || true
  echo "firewalld: 已放行 $PORT/tcp"
elif command -v ufw >/dev/null 2>&1; then
  ufw allow $PORT/tcp >/dev/null 2>&1 || true
  echo "ufw: 已放行 $PORT/tcp"
fi

echo
echo "=== ✅ 安装完成 ==="
systemctl --no-pager --lines=3 status sms-middleware || true
echo
echo "自检发一条: curl -X POST http://127.0.0.1:$PORT/sms/send -H 'Content-Type: application/json' -d '{\"to\":\"你的手机号\",\"content\":\"机房告警：联调测试\"}'"
echo "看日志:     sudo journalctl -u sms-middleware -f"
echo "改配置后:   sudo systemctl restart sms-middleware"
