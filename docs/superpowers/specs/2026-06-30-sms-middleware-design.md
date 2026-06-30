# 短信中间件设计文档（机房监控告警）

- 日期：2026-06-30
- 状态：已定稿，待评审
- 技术栈：Go（单二进制，内网 HTTP 服务）

## 1. 背景与目标

上游监控系统只能通过「短信网关」配置（HTTP/HTTPS + JSON Body）来发送告警短信，但它本身无法直接调用阿里云短信（阿里云需要 HMAC 签名 + 签名/模板）。本中间件作为**协议适配层**：把上游简单的 `{to, content}` 请求翻译为阿里云 `SendSms` 调用。

本次用途：**机房监控告警类短信**（故障/恢复通知，内容多变）。阿里云会申请一个通用告警模板做透传。

非目标（YAGNI）：
- 不做本地消息队列/重试（失败返回 5xx，由上游监控系统重发）
- 不做多模板路由（单一通用模板透传）
- 不做 HTTPS 服务（内网直连 HTTP；调阿里云仍走 HTTPS）
- 不做 Web 管理后台（纯环境变量驱动）
- 不引任何第三方依赖（仅 Go 标准库；阿里云签名手写 HMAC-SHA1）
- 不做 Docker/容器化（单二进制直接跑）

## 2. 上游系统契约（来源：截图）

上游「短信网关」配置能力：
- 协议：HTTP（也支持 HTTPS）；方法：`POST`；编码：UTF-8
- 参数位置：BODY；参数类型：JSON
- 请求体模板（固定变量）：
  ```json
  {"to": "${TELEPHONENUMBER}", "content": "${MSGCONTENT}"}
  ```
- 支持自定义查询参数/请求头/变量（本次不使用，鉴权走 IP 白名单）

即：上游会向中间件 `POST` 一个 JSON，字段固定为 `to`（手机号）与 `content`（短信正文）。

## 3. 架构

```
监控系统(上游,机房内网)              中间件(Go, :8080)                      阿里云 dysmsapi
  │ POST http://<mw>:8080/sms/send     │
  │ {"to":"...","content":"..."}       │
  │ ─────────────────────────────────► │
  │                                      │ 1. IP 白名单校验（来源=监控系统内网IP）
  │                                      │ 2. 解析 + 校验 to/content
  │                                      │ 3. 组装 SendSms：
  │                                      │      PhoneNumbers = to
  │                                      │      SignName, TemplateCode（配置）
  │                                      │      TemplateParam = {"<key>": content}
  │                                      │ 4. HMAC 签名 + HTTPS 调阿里云 ───────► │
  │                                      │ ◄──────── Code / BizId / 错误 ─────────┤
  │ ◄──────────────────────────────────  │ 5. 结构化日志（号码脱敏）
  │   HTTP 200 / 4xx / 5xx               │ 6. 按结果返回状态码
```

中间件无状态、单职责：仅做翻译转发。

## 4. 请求 / 响应契约

### 端点
- `POST /sms/send` — 发送短信
- `GET /health` — 健康检查，返回 `200 {"status":"ok"}`

### 请求
```http
POST /sms/send HTTP/1.1
Content-Type: application/json

{"to":"13800138000","content":"主机 web-01 CPU 使用率 95%，超过阈值"}
```

### 响应（上游仅按 HTTP 状态码判定）

| 情况 | 状态码 | 响应体 |
|---|---|---|
| 发送成功 | 200 | `{"code":"OK","biz_id":"..."}` |
| 来源 IP 不在白名单 | 403 | `{"code":"FORBIDDEN"}` |
| 缺 to/content 或 JSON 非法 | 400 | `{"code":"BAD_REQUEST","message":"..."}` |
| 阿里云业务错误（限流/号码非法等） | 502 | `{"code":"UPSTREAM_ERROR","aliyun_code":"...","message":"..."}` |
| 调阿里云网络错误/超时 | 502 | `{"code":"UPSTREAM_UNAVAILABLE"}` |

约定：`2xx` 视为成功；`4xx/5xx` 视为失败，上游据此重发。

## 5. 阿里云映射（通用模板透传）

阿里云必须用 签名(SignName) + 模板(TemplateCode)，不能发任意正文。透传方案：
- 阿里云控制台申请 1 个**通知/告警类**模板，模板内容含单变量：`${content}`（可加少量固定文字如 `机房告警：${content}`，变量名须为 `content`，或在配置里改 `template_param_key`）。
- 每次调用：`TemplateParam = {"<template_param_key>": <整段 content>}`，`PhoneNumbers = <to>`，`SignName` 与 `TemplateCode` 来自配置。
- 手机号直接透传（阿里云国内短信接收 11 位号码）。

> 注：模板审核合规属于用户侧；中间件逻辑与变量名可通过配置适配。

## 6. 项目结构（最简版）

```
sms-middleware/
├── go.mod                      # module sms-middleware，go 1.22+
├── main.go                     # 配置(环境变量) + HTTP 服务 + 路由 + 处理器 + IP 白名单 + 日志
├── aliyun.go                   # SendSms：手写 RPC 签名(HMAC-SHA1) + HTTPS 调用
├── main_test.go                # 单元测试（IP 白名单、校验、组装、脱敏）
├── config.example.env          # 环境变量配置样例
└── README.md                   # 上游对接 + 阿里云配置说明
```

### 关键决策（零第三方依赖）
- **阿里云调用**：不引 SDK，手写 RPC 签名。用标准库 `crypto/hmac`+`crypto/sha1`+`encoding/base64`+`net/url`+`net/http`，对 `dysmsapi.aliyuncs.com`（Version `2017-05-25`，Action `SendSms`）按阿里云 RPC 签名规范（参数排序拼接 → HMAC-SHA1 → base64）签名后 GET 调用。签名逻辑单文件可审计。
- **HTTP 服务**：标准库 `net/http`，`http.ServeMux`（Go 1.22+ 方法+路径模式：`POST /sms/send`、`GET /health`），设置读写与整体超时。
- **IP 白名单**：解析 `RemoteAddr`；支持单 IP 与 CIDR（`net/netip`）。**失败关闭**：白名单为空时拒绝所有请求并返回 403、打印告警日志。
- **日志**：标准库 `log/slog`（结构化 JSON），号码脱敏为 `138****8000`，内容只记长度。
- **配置**：全部从环境变量读取（见 §7），无配置文件、无解析依赖。

## 7. 配置（环境变量）

| 环境变量 | 说明 | 默认/示例 |
|---|---|---|
| `LISTEN` | HTTP 监听地址 | `:8080` |
| `IP_ALLOWLIST` | 来源 IP/CIDR 白名单，逗号分隔 | `10.0.0.0/8,192.168.0.0/16` |
| `ALIYUN_REGION` | 阿里云地域 | `cn-hangzhou` |
| `ALIYUN_ACCESS_KEY_ID` | AccessKey ID | 必填 |
| `ALIYUN_ACCESS_KEY_SECRET` | AccessKey Secret | 必填 |
| `ALIYUN_SIGN_NAME` | 签名名称 | `监控告警` |
| `ALIYUN_TEMPLATE_CODE` | 模板 CODE | `SMS_xxxxxxxxx` |
| `ALIYUN_TEMPLATE_PARAM_KEY` | 模板变量名 | `content` |
| `ALIYUN_TIMEOUT` | 调阿里云超时 | `5s` |

## 8. 错误处理与日志

- 每个失败分支都有明确状态码与响应体，不吞错。
- 每次请求记录：时间、来源 IP、手机号（脱敏）、内容长度、阿里云返回 Code/BizId、耗时(ms)、最终 HTTP 状态。
- 阿里云调用超时（默认 5s）→ `502 UPSTREAM_UNAVAILABLE`。
- 服务优雅关闭：收到 `SIGINT/SIGTERM` 后停止接收新请求，等待在途请求完成（固定上限 15s）后退出。

## 9. 机房告警场景说明

- **并发突发**：故障可能瞬间触发大量告警。Go 原生高并发，无需额外队列。
- **阿里云限流**：同一号码有频率限制（约 1 条/分钟级），突发可能触发 `isv.BUSINESS_LIMIT_CONTROL`，中间件返回 `502`，由上游按状态码重发。
- **可靠性边界**：中间件无状态、不重试；重发是上游监控系统的职责（多数监控系统天然支持告警重发）。

## 10. 测试策略

- 单元测试（不联网）：
  - IP 白名单：单 IP / CIDR 命中与拒绝、空名单策略。
  - 请求校验：缺 `to` / 缺 `content` / 非 JSON → 400。
  - 组装：`content` → `TemplateParam` 映射、`template_param_key` 可配置。
  - 状态码映射：Aliyun `Code=="OK"` → 200；其他 → 502 且带 `aliyun_code`。
  - 号码脱敏函数。
- Aliyun：把「组装+签名生成请求 URL/参数」与「发 HTTP」拆开；单测覆盖参数组装与签名的确定性/格式（给定固定入参产出稳定签名），HTTP 调用注入可替换的 `http.Client`，不真实联网。
- 集成验证：提供 `curl` 示例；用真实凭证端到端发一条告警短信确认。

## 11. 部署

- 编译：`go build -o sms-middleware`，得到单二进制。
- 运行：设置环境变量后 `./sms-middleware`（或 `source config.example.env && ./sms-middleware`）。
- README 说明：
  - 上游「短信网关」如何填：HTTP 地址 = `http://<中间件内网IP>:8080/sms/send`，请求体 = `{"to":"${TELEPHONENUMBER}","content":"${MSGCONTENT}"}`。
  - 白名单 IP：填监控系统所在服务器/网段到 `IP_ALLOWLIST`。
  - 阿里云：AccessKey、SignName、TemplateCode 如何准备并填入环境变量。

## 12. 待用户提供的输入（实现/联调阶段）

- 阿里云 `AccessKeyId` / `AccessKeySecret`
- `SignName`（签名名称）
- `TemplateCode`（已审核通过的告警模板 CODE）
- 监控系统来源 IP / 内网网段（用于白名单）
- 中间件监听端口与部署机器
