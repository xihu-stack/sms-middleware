#!/usr/bin/env bash
# 短信中间件 Linux 一键安装脚本
# 用法：把编译好的 sms-middleware 二进制和本脚本放到同一目录，然后：
#   sudo bash install.sh
# 脚本会：写配置 → 安装 systemd 服务 → 启动 → 放行端口 → 给出自检命令。
# 幂等：可重复运行，每次都会用你输入的新值更新配置并重启服务。

set -euo pipefail

INSTALL_DIR=/opt/sms-middleware
SERVICE_FILE=/etc/systemd/system/sms-middleware.service
PORT="${LISTEN_PORT:-8080}"

# ---- 前置检查 ----
if [ "$(id -u)" -ne 0 ]; then
  echo "请用 root 或 sudo 运行：sudo bash install.sh" >&2
  exit 1
fi

BIN_SRC="$(dirname "$(readlink -f "$0")")/sms-middleware"
if [ ! -f "$BIN_SRC" ]; then
  echo "未找到 sms-middleware 二进制（应在脚本同目录）。" >&2
  echo "请先把 sms-middleware 放到: $(dirname "$BIN_SRC")" >&2
  exit 1
fi

# ---- 交互收集配置（回车保留旧值/默认值）----
ask() { # ask "提示" "默认值"  -> 读到 $ANS
  local prompt="$1" def="${2-}" v
  read -rp "$prompt [${def:+$def}]: " v || v=""
  ANS="${v:-$def}"
}
ask_secret() { # 静默读取，回车保留旧值
  local prompt="$1" def="${2-}" v
  if [ -n "$def" ]; then
    read -rp "$prompt [回车保留已存的值]: " v || v=""
    ANS="${v:-$def}"
  else
    read -rsp "$prompt: " v; echo
    ANS="$v"
  fi
}

echo "=== 配置阿里云短信（回车保留括号内默认值）==="
oldval() { [ -f "$INSTALL_DIR/config.env" ] && grep -m1 -E "^$1=" "$INSTALL_DIR/config.env" 2>/dev/null | cut -d= -f2- || true; }
OLD_AK="$(oldval ALIYUN_ACCESS_KEY_ID)"
OLD_SK="$(oldval ALIYUN_ACCESS_KEY_SECRET)"
OLD_SIGN="$(oldval ALIYUN_SIGN_NAME)"
OLD_TPL="$(oldval ALIYUN_TEMPLATE_CODE)"
OLD_TPL_KEY="$(oldval ALIYUN_TEMPLATE_PARAM_KEY)"; [ -z "$OLD_TPL_KEY" ] && OLD_TPL_KEY=content
OLD_IP="$(oldval IP_ALLOWLIST)"
OLD_REGION="$(oldval ALIYUN_REGION)"; [ -z "$OLD_REGION" ] && OLD_REGION=cn-hangzhou

ask       "AccessKey ID"        "${OLD_AK}";        AK="$ANS"
ask_secret "AccessKey Secret"   "${OLD_SK}";        SK="$ANS"
ask       "签名 SignName"       "${OLD_SIGN}";      SIGN="$ANS"
ask       "模板 TemplateCode"   "${OLD_TPL}";       TPL="$ANS"
ask       "模板变量名"           "${OLD_TPL_KEY:-content}"; TPL_KEY="$ANS"
ask       "IP白名单(逗号分隔CIDR/IP)" "${OLD_IP:-}";  IP="$ANS"
ask       "地域 Region"         "${OLD_REGION}";    REGION="$ANS"
ask       "监听端口"            "$PORT";            PORT="$ANS"

if [ -z "$AK" ] || [ -z "$SK" ] || [ -z "$SIGN" ] || [ -z "$TPL" ]; then
  echo "AccessKey/签名/模板不能为空，已中止。" >&2; exit 1
fi

# ---- 安装二进制 + 写配置 ----
mkdir -p "$INSTALL_DIR"
install -m 0755 "$BIN_SRC" "$INSTALL_DIR/sms-middleware"

cat > "$INSTALL_DIR/config.env" <<EOF
LISTEN=:$PORT
IP_ALLOWLIST=$IP
ALIYUN_REGION=$REGION
ALIYUN_ACCESS_KEY_ID=$AK
ALIYUN_ACCESS_KEY_SECRET=$SK
ALIYUN_SIGN_NAME=$SIGN
ALIYUN_TEMPLATE_CODE=$TPL
ALIYUN_TEMPLATE_PARAM_KEY=$TPL_KEY
ALIYUN_TIMEOUT=5s
ALIYUN_ENDPOINT=https://dysmsapi.aliyuncs.com
EOF
chmod 600 "$INSTALL_DIR/config.env"
echo "已写入 $INSTALL_DIR/config.env"

# ---- 安装 systemd 服务 ----
cat > "$SERVICE_FILE" <<EOF
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

# ---- 放行端口（尽力而为）----
if command -v firewall-cmd >/dev/null 2>&1; then
  firewall-cmd --add-port="$PORT"/tcp --permanent >/dev/null 2>&1 || true
  firewall-cmd --reload >/dev/null 2>&1 || true
  echo "firewalld: 已放行 $PORT/tcp"
elif command -v ufw >/dev/null 2>&1; then
  ufw allow "$PORT"/tcp >/dev/null 2>&1 || true
  echo "ufw: 已放行 $PORT/tcp"
fi

# ---- 结果 ----
echo
echo "=== 安装完成 ==="
systemctl --no-pager --lines=5 status sms-middleware || true
echo
echo "查看日志:   sudo journalctl -u sms-middleware -f"
echo "重启服务:   sudo systemctl restart sms-middleware"
echo "自检发一条: curl -X POST http://127.0.0.1:$PORT/sms/send -H 'Content-Type: application/json' -d '{\"to\":\"你的手机号\",\"content\":\"部署自检\"}'"
