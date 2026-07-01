# 短信中间件（机房监控告警）

把上游系统简单的 `{to, content}` HTTP 请求，翻译成**阿里云短信** `SendSms` 调用。

- 纯 Go 标准库实现，**零第三方依赖**
- 单二进制，配置走环境变量
- 对内网暴露 HTTP；调阿里云走 HTTPS
- 鉴权：IP 白名单（来源 IP 不在白名单即拒绝）

## 适合场景

上游监控系统只能按「短信网关」(HTTP POST + JSON Body) 配置发短信，但无法直接调用阿里云（需要 HMAC 签名 + 签名/模板）。本中间件做协议适配：上游 `POST` 一个 `{"to","content"}`，中间件用阿里云**通用模板透传**把整段 `content` 作为模板变量发出。

## 快速开始：一条命令部署

在 Linux 服务器上运行这一行，它会**自动下载二进制 → 写配置 → 装 systemd 服务 → 启动**。无需装 Go、无需 clone、无需编译：

```bash
curl -fsSL https://raw.githubusercontent.com/xihu-stack/sms-middleware/main/install.sh | sudo bash
```

> 不确定服务器是否满足要求？先跑预检（只读、不改系统）：
> ```bash
> curl -fsSL https://raw.githubusercontent.com/xihu-stack/sms-middleware/main/preflight.sh | bash
> ```
> 检查架构、资源、curl/systemd、以及能否连通阿里云（出站 443）和 GitHub。

脚本只问 5 项（其余用默认值）：
- AccessKey ID / Secret
- 签名 SignName、模板 TemplateCode
- IP 白名单（监控系统出口 IP / 网段）

完成后自动启动并打印自检命令。

### 前置条件（先准备好）
- 一台 Linux 服务器（能访问阿里云 `dysmsapi.aliyuncs.com`）
- 阿里云短信就绪（详见下文「阿里云配置」）：**签名 SignName**、**模板 TemplateCode**（内容含 `${content}`）、**RAM 子账号 AccessKey**
- 知道**监控系统出口 IP / 内网网段**

### 验证
```bash
curl http://127.0.0.1:8080/health        # 返回 {"status":"ok"} 即服务正常
curl -X POST http://127.0.0.1:8080/sms/send -H 'Content-Type: application/json' \
  -d '{"to":"你的手机号","content":"机房告警：联调测试"}'   # 返回 {"code":"OK",...}
```
手机收到短信 = **部署成功** 🎉（若返回 `502`，看响应里的 `aliyun_code` 对照下表）

### 接入监控系统
在监控系统「短信网关」配置里填：
- **地址**：`http://<中间件服务器IP>:8080/sms/send`
- **请求体**：`{"to":"${TELEPHONENUMBER}","content":"${MSGCONTENT}"}`
- 确保该监控系统的出口 IP 已加入中间件的 `IP_ALLOWLIST`

### 常见报错
| 返回 | 处理 |
|---|---|
| `isv.SMS_TEMPLATE_ILLEGAL` | 核对 TemplateCode、`ALIYUN_TEMPLATE_PARAM_KEY` 与模板变量名一致 |
| `isv.SMS_SIGNATURE_ILLEGAL` | 核对 SignName，确认签名已过审 |
| `SignatureDoesNotMatch` / `InvalidAccessKeyId.NotFound` | AccessKey / Secret 填错 |
| `isv.BUSINESS_LIMIT_CONTROL` | 同号码限流（约 1 条/分钟），稍后重发 |
| `isv.MOBILE_NUMBER_ILLEGAL` | 用 11 位国内号 |
| HTTP `403` | 监控出口 IP 不在白名单，加入 `IP_ALLOWLIST` 后 `sudo systemctl restart sms-middleware` |

> 改配置：编辑 `/opt/sms-middleware/config.env` 后 `sudo systemctl restart sms-middleware`，或直接重跑安装脚本。

<details><summary>离线 / 自编译部署（无外网或想自己编译时用）</summary>

```bash
git clone https://github.com/xihu-stack/sms-middleware.git && cd sms-middleware
go build -o sms-middleware       # 需 Go ≥ 1.22
sudo bash install.sh             # 当前目录有 sms-middleware 时优先用本地的，不会联网下载
```
或在 Windows 交叉编译：`.\build.ps1` 生成 `sms-middleware-linux-amd64`，连同 `install.sh` 上传服务器后 `sudo bash install.sh`。

</details>

---

## 请求 / 响应

### 发送短信
```
POST /sms/send
Content-Type: application/json

{"to":"13800138000","content":"主机 web-01 CPU 95%，超过阈值"}
```

| 情况 | 状态码 | 响应体 |
|---|---|---|
| 成功 | 200 | `{"code":"OK","biz_id":"...","request_id":"..."}` |
| 来源 IP 不允许 | 403 | `{"code":"FORBIDDEN"}` |
| 参数缺失/JSON 非法 | 400 | `{"code":"BAD_REQUEST","message":"..."}` |
| 阿里云业务错误（限流等） | 502 | `{"code":"UPSTREAM_ERROR","aliyun_code":"...","message":"..."}` |
| 调阿里云网络/超时 | 502 | `{"code":"UPSTREAM_UNAVAILABLE"}` |

上游按 **HTTP 状态码** 判定成功/失败（2xx 成功，4xx/5xx 失败可重发）。

### 健康检查
```
GET /health  ->  200 {"status":"ok"}
```

## 配置（环境变量）

见 `config.example.env`。必填项：

| 变量 | 说明 |
|---|---|
| `ALIYUN_ACCESS_KEY_ID` | 阿里云 AccessKey Id |
| `ALIYUN_ACCESS_KEY_SECRET` | 阿里云 AccessKey Secret |
| `ALIYUN_SIGN_NAME` | 短信签名名称 |
| `ALIYUN_TEMPLATE_CODE` | 短信模板 CODE |
| `IP_ALLOWLIST` | 监控系统来源 IP/CIDR，逗号分隔 |

可选项（带默认值）：`LISTEN=:8080`、`ALIYUN_REGION=cn-hangzhou`、`ALIYUN_TEMPLATE_PARAM_KEY=content`、`ALIYUN_TIMEOUT=5s`、`ALIYUN_ENDPOINT=https://dysmsapi.aliyuncs.com`。

> `IP_ALLOWLIST` 为空时**全部拒绝**（失败关闭），因为白名单是唯一鉴权手段。

## 阿里云侧准备（通用模板透传）

阿里云必须用「签名 + 模板」发短信，不能发任意正文。透传方案：

1. 在阿里云短信控制台申请一个**通知/告警类**模板，模板内容含单变量，例如：
   - `${content}` ，或
   - `机房告警：${content}`
   变量名用 `content`（或在中间件侧改 `ALIYUN_TEMPLATE_PARAM_KEY` 与之一致）。
2. 记下**签名名称**（`SignName`）和**模板 CODE**（`TemplateCode`），填入环境变量。
3. 每次调用，中间件会把整段 `content` 作为该变量的值发出。

> 模板审核合规属用户侧；中间件逻辑与变量名可通过配置适配。

## 编译与运行

```bash
# 编译（生成单二进制 sms-middleware.exe / sms-middleware）
go build -o sms-middleware

# 设置环境变量后运行（示例，PowerShell）
$env:LISTEN=":8080"
$env:IP_ALLOWLIST="10.0.0.0/8,192.168.0.0/16"
$env:ALIYUN_ACCESS_KEY_ID="..."
$env:ALIYUN_ACCESS_KEY_SECRET="..."
$env:ALIYUN_SIGN_NAME="监控告警"
$env:ALIYUN_TEMPLATE_CODE="SMS_xxxxxxxxx"
./sms-middleware
```

日志为结构化 JSON（stdout）：成功记 `to=138****8000 len=.. biz_id=..`，失败记阿里云 `code`/`message`。号码自动脱敏。

## 上游系统（短信网关）如何配置

在你的**监控系统 / 源头系统**的"短信网关"里，把中间件当作一个 HTTP 短信网关来填：

| 配置字段 | 填什么 |
|---|---|
| 接口类型 | `HTTP` |
| 发送方式 | `POST` |
| 编码格式 | `UTF-8` |
| 参数位置 | `BODY` |
| 参数类型 | `JSON` |
| TLS 版本 | HTTP 用不到，留默认 |
| 查询参数 | 留空 |
| 请求头 | 留空（或加一行 `Content-Type: application/json`）|
| **HTTP 地址** | `http://<中间件服务器IP>:8080/sms/send` |
| **请求体** | `{"to":"${TELEPHONENUMBER}","content":"${MSGCONTENT}"}` |

**两个要点：**
1. **地址里的 IP** 用监控系统能访问到中间件的那个 IP：同机用 `127.0.0.1`；同内网用内网 IP；跨网段用可路由 IP。
2. **请求体两个变量必须一字不差**：`${TELEPHONENUMBER}`（手机号）、`${MSGCONTENT}`（短信内容）是监控系统自带的固定变量，发送时自动替换。键名 `to` / `content` 是中间件识别的，不要改。

监控系统替换变量后实际发出的请求体形如：
```json
{"to":"13800138000","content":"主机 web-01 CPU 95%，超过阈值"}
```

### ⚠️ 必做：把监控系统的 IP 加进白名单（否则 403）
中间件只放行 `IP_ALLOWLIST` 里的来源。如果不知道监控系统出口 IP，用"反向查 IP"最省事：

1. 在监控系统配好上面的网关，**触发一次告警 / 测试发送**。
2. 到中间件服务器看拒绝日志：
   ```bash
   sudo journalctl -u sms-middleware -g "denied" --no-pager
   ```
   会看到类似 `msg="request denied by ip allowlist" remote=10.0.0.50:xxxxx` —— `10.0.0.50` 就是监控系统的出口 IP。
3. 加进白名单并重启：
   ```bash
   sudo vi /opt/sms-middleware/config.env     # IP_ALLOWLIST=10.0.0.0/8,10.0.0.50
   sudo systemctl restart sms-middleware
   ```
4. 监控系统再发一次 → 成功。

### 验证整条链路
监控系统触发告警时，中间件上实时看日志：
```bash
sudo journalctl -u sms-middleware -f
```
看到 `http request ... path=/sms/send status=200` 紧接 `sms sent` + 手机收到告警 = **正式上线**。

> 若监控系统强制只填 `https://`，而中间件是 HTTP：放同一台机器用 `127.0.0.1`，或在中间件前加 nginx 做 HTTPS 反代。

## 测试

```bash
go test ./...
```

测试覆盖：阿里云 RPC 签名（含独立计算的黄金值）、`percentEncode`、IP 白名单（含失败关闭）、参数校验、状态码映射、模板变量名可配置、号码脱敏。`SendSms` 用 `httptest` 桩验证，不真实联网。

## 联调（真实发一条）

填好真实环境变量后，用 curl 验证：

```bash
curl -X POST http://127.0.0.1:8080/sms/send \
  -H "Content-Type: application/json" \
  -d '{"to":"13800138000","content":"机房告警：测试短信"}'
```

返回 `200 {"code":"OK",...}` 且手机收到短信即联调成功。若返回 502，看响应体里的 `aliyun_code` 对照阿里云错误码排查（常见：`isv.BUSINESS_LIMIT_CONTROL` 限流、`isv.SMS_TEMPLATE_ILLEGAL` 模板不符、`SignatureNonceUsed` 重放等）。

## 日志与排错

服务输出结构化日志到 stdout，systemd 下由 journald 收集。**查看日志：**
```bash
sudo journalctl -u sms-middleware -f             # 实时跟踪（最常用）
sudo journalctl -u sms-middleware --since "10 min ago"
sudo journalctl -u sms-middleware -p err          # 只看 ERROR
sudo journalctl -u sms-middleware -g "aliyun"     # 按关键字过滤
```

**每个请求都有一行访问日志**（状态 ≥400 自动降为 WARN）：
```
level=INFO msg="http request" remote=10.0.0.5:54321 method=POST path=/sms/send status=200 bytes=58 duration_ms=420
```

**关键日志含义**：
| msg 字段 | 含义 |
|---|---|
| `http request` | 每个请求一行：来源 IP / 方法 / 路径 / 状态 / 总耗时 |
| `sms sent` | 发送成功：号码(脱敏)、内容长度、biz_id、`aliyun_ms` |
| `bad request` | 400 参数错：原因（JSON 非法 / `to` 空 / `content` 空） |
| `request denied by ip allowlist` | 403：来源 IP 不在白名单 |
| `aliyun business error` | 阿里云业务错：`code`/`message`/`request_id`/`aliyun_ms` |
| `aliyun call failed` | 调阿里云网络/超时：`err` + `aliyun_ms` |

**快速定位问题**：
- **收不到短信** → `journalctl -g "sms sent"` 看有没有成功；没有就顺次查 `bad request` / `denied` / `aliyun business error`，定位卡在哪一步。
- **慢** → 对比 `duration_ms`（总耗时）和 `aliyun_ms`（阿里云调用耗时），判断是阿里云慢还是网络慢。
- 号码自动脱敏（如 `138****8000`）；短信内容只记长度，不记明文，避免泄露。

## 注意事项

- **限流**：同一号码约 1 条/分钟，告警突发可能触发 `isv.BUSINESS_LIMIT_CONTROL`，返回 502 由上游重发；中间件无状态、不本地排队。
- **重试**：由上游监控系统负责（多数监控系统天然支持告警重发）。
- **手机号**：直接透传，国内短信接收 11 位号码。

## 部署到 Linux（systemd）

中间件是纯标准库 Go，无外部依赖，**可在 Windows 上交叉编译出 Linux 二进制**直接丢到服务器跑。

### 最简流程（推荐：2 条命令）

**Windows 上一键编译：**
```powershell
.\build.ps1      # 生成 sms-middleware-linux-amd64 / -arm64（无依赖）
```
**把二进制和安装脚本传到服务器：**
```bash
scp sms-middleware-linux-amd64 install.sh user@服务器IP:/tmp/
```
**服务器上一键安装**（交互式填阿里云配置，自动写配置 + 装 systemd 服务 + 启动 + 放行端口）：
```bash
ssh user@服务器IP
cd /tmp && mv sms-middleware-linux-amd64 sms-middleware
sudo bash install.sh
```
完成。`install.sh` 幂等，改配置重跑即可。下方是它自动化掉的手动步骤，留作参考。

---

### 1) 交叉编译（在当前 Windows 机器上）
```powershell
$env:Path = "C:\Program Files\Go\bin;" + $env:Path
# 先确认服务器架构：x86_64 -> amd64，aarch64 -> arm64
$env:GOOS="linux"; $env:GOARCH="amd64"
go build -o sms-middleware
# 还原（避免影响后续 Windows 构建）
Remove-Item Env:\GOOS, Env:\GOARCH
```
得到无依赖的 ELF 二进制 `sms-middleware`。

### 2) 上传到服务器
把 `sms-middleware`、`config.example.env`、`deploy/sms-middleware.service` 传到 Linux，例如放 `/opt/sms-middleware/`：
```bash
sudo mkdir -p /opt/sms-middleware
sudo cp sms-middleware /opt/sms-middleware/
sudo chmod +x /opt/sms-middleware/sms-middleware
sudo cp config.example.env /opt/sms-middleware/config.env
```

### 3) 填写配置（编辑 config.env，见下文「阿里云配置」）
```bash
sudo chmod 600 /opt/sms-middleware/config.env   # 含密钥，限制读写
sudo nano /opt/sms-middleware/config.env
```

### 4) 注册 systemd 服务（开机自启 + 自动重启）
```bash
sudo cp deploy/sms-middleware.service /etc/systemd/system/
# 若用非 nobody/nogroup 的运行用户，编辑 service 里的 User/Group
sudo systemctl daemon-reload
sudo systemctl enable --now sms-middleware
sudo systemctl status sms-middleware         # 看是否 active (running)
sudo journalctl -u sms-middleware -f          # 实时看日志
```
改完配置后重启：`sudo systemctl restart sms-middleware`

### 5) 放行端口（按需）
```bash
# firewalld（RHEL/CentOS）
sudo firewall-cmd --add-port=8080/tcp --permanent && sudo firewall-cmd --reload
# ufw（Ubuntu）—— 只允许内网网段
sudo ufw allow from 10.0.0.0/8 to any port 8080 proto tcp
```

### 6) 在服务器上验证
```bash
curl -X POST http://127.0.0.1:8080/sms/send \
  -H 'Content-Type: application/json' \
  -d '{"to":"13800138000","content":"机房告警：部署自检"}'
```
返回 `200 {"code":"OK",...}` 且手机收到即成功；502 看响应里 `aliyun_code` 对照阿里云错误码。

> 提示：若想监听 80 等特权端口，把 `LISTEN=:80` 并给二进制赋权 `sudo setcap 'cap_net_bind_service=+ep' /opt/sms-middleware/sms-middleware`（service 里用 root 或保留 capability）。

## 阿里云配置（在控制台获取）

中间件需要 4 个值：`ALIYUN_ACCESS_KEY_ID`、`ALIYUN_ACCESS_KEY_SECRET`、`ALIYUN_SIGN_NAME`、`ALIYUN_TEMPLATE_CODE`。获取步骤：

### 1) 开通短信服务 + 申请签名（SignName）
- 控制台进入「短信服务」（https://dysms.console.aliyun.com ）
- **国内消息 → 签名管理 → 添加签名**
- 签名名称即 `ALIYUN_SIGN_NAME`（如 `监控告警` 或公司简称）；需提交资质（企业营业执照、授权书等）审核，通过后才能用。

### 2) 申请模板（TemplateCode）
- **国内消息 → 模板管理 → 添加模板**
- 模板类型选 **通知短信**（告警类归此类）
- 模板内容填单个变量，例如：`${content}` 或 `机房告警：${content}`
- 审核通过后得到模板 CODE，形如 `SMS_123456789` → 填入 `ALIYUN_TEMPLATE_CODE`
- 变量名 `content` 对应 `ALIYUN_TEMPLATE_PARAM_KEY=content`（两者必须一致）

> 若纯 `${content}` 审核不通过，改成带固定文字的形式（如 `【你的业务】告警：${content}`），只要保留 `content` 变量即可，中间件无需改动。

### 3) 创建 AccessKey（务必用 RAM 子账号，不要用主账号）
- 控制台 → 「访问控制 RAM」→ 用户 → **创建用户**
- 勾选 **编程访问**（OpenAPI 调用方式）
- 创建后**立即保存** AccessKey ID 和 Secret（Secret 只显示一次！）
- 给该用户授权策略：`AliyunDysmsFullAccess`（短信发送权限）
  - 更严格可自定义策略，只允许 `dysms:SendSms`，最小权限

### 4) 地域（Region）
- 国内短信默认 `ALIYUN_REGION=cn-hangzhou`；端点 `dysmsapi.aliyuncs.com` 已内置，一般无需改动。

### 5) 回填到 config.env
```
ALIYUN_ACCESS_KEY_ID=你的AccessKeyId
ALIYUN_ACCESS_KEY_SECRET=你的AccessKeySecret
ALIYUN_SIGN_NAME=监控告警
ALIYUN_TEMPLATE_CODE=SMS_xxxxxxxxx
ALIYUN_TEMPLATE_PARAM_KEY=content
ALIYUN_REGION=cn-hangzhou
```
保存后 `sudo systemctl restart sms-middleware` 生效。

**安全建议**：`config.env` 做 `chmod 600`；用 RAM 子账号而非主账号 AccessKey；`IP_ALLOWLIST` 只放监控系统来源 IP/网段。
