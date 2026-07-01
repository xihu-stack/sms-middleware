#!/usr/bin/env bash
# 预检：在候选服务器上运行，确认是否满足短信中间件部署要求。
# 只读、不改系统、无需 root。
# 用法：curl -fsSL https://raw.githubusercontent.com/xihu-stack/sms-middleware/main/preflight.sh | bash
set -uo pipefail

ok(){   printf "  \033[32m✓\033[0m %s\n" "$1"; }
no(){   printf "  \033[31m✗\033[0m %s\n" "$1"; }
info(){ printf "  · %s\n" "$1"; }

echo "=== 1) 系统与架构（需 Linux x86_64 或 aarch64）==="
arch="$(uname -m)"
case "$arch" in
  x86_64)        ok "$arch -> 用 amd64 二进制" ;;
  aarch64|arm64) ok "$arch -> 用 arm64 二进制" ;;
  *)             no "$arch 架构不支持（需 x86_64 或 aarch64）" ;;
esac
grep -E '^(NAME|VERSION)=' /etc/os-release 2>/dev/null | sed 's/^/  · /' || info "无法读取发行版信息"

echo "=== 2) 权限（安装需要 root/sudo）==="
if [ "$(id -u)" -eq 0 ]; then ok "当前是 root"
elif sudo -n true 2>/dev/null; then ok "可用 sudo（免密）"
else info "当前非 root；部署时用 sudo 跑安装脚本即可"; fi

echo "=== 3) 资源（需求极低，几乎任何机器都够）==="
free -h 2>/dev/null | awk 'NR==2{print "  · 内存总量: "$2}' || true
df -h /  2>/dev/null | awk 'NR==2{print "  · 根分区可用: "$4}' || true
nproc 2>/dev/null | awk '{print "  · CPU 核数: "$1}' || true

echo "=== 4) 依赖命令（下载/安装需要 curl 或 wget）==="
if command -v curl >/dev/null; then ok "curl 已安装: $(command -v curl)"
elif command -v wget >/dev/null; then ok "wget 已安装: $(command -v wget)"
else no "既无 curl 也无 wget，需先安装其一"; fi

echo "=== 5) systemd（服务托管用）==="
if command -v systemctl >/dev/null; then ok "systemd: $(systemctl --version 2>/dev/null | head -1)"
else no "无 systemd（可改用 nohup 直接跑二进制：nohup ./sms-middleware &）"; fi

echo "=== 6) 出站到阿里云短信（必须，发短信用）==="
code="$(curl -fsS -m 8 -o /dev/null -w '%{http_code}' https://dysmsapi.aliyuncs.com/ 2>/dev/null || echo FAIL)"
if [ "$code" != "FAIL" ]; then ok "可达 dysmsapi.aliyuncs.com:443 (HTTP $code)"
else no "连不上阿里云 dysmsapi.aliyuncs.com:443 —— 必须放行出站 443"; fi

echo "=== 7) 出站到 GitHub（仅安装时下载二进制需要）==="
code="$(curl -fsS -m 8 -o /dev/null -w '%{http_code}' https://github.com/ 2>/dev/null || echo FAIL)"
if [ "$code" != "FAIL" ]; then ok "可达 github.com:443 (HTTP $code) -> 可一键安装"
else no "连不上 GitHub —— 改用离线部署：在能联网的机器编译后 scp 二进制+install.sh"; fi

echo
echo "=== 8) 入站端口（部署后监控系统访问中间件用）==="
info "默认监听 TCP 8080（部署后可改 config.env 的 LISTEN 后重启）"
info "需放行：本机防火墙(firewalld/ufw) + 云控制台安全组"
info "部署后自测：本机 curl http://127.0.0.1:8080/health"
info "从监控系统机：curl http://<本机IP>:8080/health 应返回 {\"status\":\"ok\"}"
echo
echo "预检完成。上面没有 ✗ 即可一键部署。"
