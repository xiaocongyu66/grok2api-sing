# Grok2API 安全与逻辑审计报告

> **审计对象**：`/tmp/grok2api-push`（Grok2API 网关）  
> **审计视角**：逆向工程 / 代码审计（静态分析）  
> **审计日期**：2026-07-16  
> **范围**：后端鉴权边界、凭据与密钥、SSRF、媒体公开面、选号/重试/计费逻辑、部署默认配置  
> **不在范围**：第三方 `sing-box` 源码深度审计、前端 XSS 专项、动态渗透 exploit

本报告仅供安全研究与加固参考。请遵守 Grok / xAI 服务条款及当地法律法规。

---

## 1. 项目概览

Grok2API 将 Grok Build OAuth、Grok Web SSO、Grok Console SSO 组织为独立账号池，对外提供 OpenAI 风格接口、Anthropic Messages 兼容接口，以及管理后台。

| 层级 | 路径 | 职责 |
| --- | --- | --- |
| 传输层 | `internal/transport/http` | Gin 路由、Admin JWT、Client Key、公开媒体、静态前端 |
| 应用层 | `internal/application/*` | 账号池、选号、计费预留、审计、导入导出 |
| Provider | `internal/infra/provider/{cli,web,console}` | 上游协议适配（含 Statsig、Device OAuth、SSO 转换） |
| 出口 | `internal/infra/egress` | 代理池、浏览器 TLS 指纹、sing-box 出站 |
| 安全原语 | `internal/infra/security` | AES-GCM 凭据加密、bcrypt 管理员密码、JWT、Key 哈希 |
| 运行态 | `internal/infra/runtime/{memory,redis}` | RPM 限流、并发租约、粘滞会话、分布式锁 |

**核心资产：**

- 上游 OAuth / SSO 明文（落库为 AES-256-GCM 密文）
- 客户端 API Key（SHA-256 哈希 + 可逆密文副本）
- 管理员会话（JWT Access + 可轮换 Refresh）
- 生成图片的公开 URL
- 出口代理与账号调度状态

---

## 2. 鉴权边界一览

```text
公开（无鉴权）
  GET  /healthz
  GET  /readyz
  GET  /swagger/*          # 仅 swaggerEnabled=true
  GET  /v1/media/images/:id
  SPA  静态资源

管理端
  POST /api/admin/v1/auth/login|refresh|logout   # 公开
  *    /api/admin/v1/*                           # AdminAuth (Bearer JWT)

客户端 API
  *    /v1/*                                     # ClientAuth (Bearer / X-API-Key)
       另有全局限流闸 + TrafficReady 启动门闩
```

**做得好的点：**

- Admin / Client / Public 路由分组清晰
- Refresh Cookie：`HttpOnly`、`SameSite=Strict`、路径限制在 `/api/admin/v1/auth`
- Client Key 校验使用 `subtle.ConstantTimeCompare`
- 登录对不存在用户走 dummy bcrypt，降低时序侧信道
- 审计尝试记录会剥离 Bearer / Cookie / 疑似 secret / URL 中的敏感段

---

## 3. 安全发现

### 3.1 严重度定义

| 级别 | 含义 |
| --- | --- |
| **高** | 可导致内网探测、大规模凭据外泄或默认供应链风险 |
| **中** | 在常见部署/失陷条件下扩大爆炸半径或削弱控制面 |
| **低** | 纵深防御缺口、加固建议 |
| **信息** | 探测面、运维可见性 |

---

### 3.2 [高] 对话图片下载：DNS 重绑定 SSRF

**位置：** `internal/infra/provider/web/attachments.go`

**机制：**

1. `validateRemoteImageURL` 对主机名做 `LookupIPAddr`，要求结果均为公网地址。
2. 校验通过后只返回 `*url.URL`，**不固定解析到的 IP**。
3. `loadChatImage` 随后通过 `lease.Do(request)` 发起真实 HTTP 请求，**再次解析 DNS**。

**攻击条件：**

- 攻击者持有合法 Client API Key
- 请求中携带可控图片 URL（HTTPS）
- 攻击者控制域名的权威 DNS，在两次解析之间切换 A 记录

**影响：**

- 经网关出口（含代理池）访问内网 / 链路本地 / 云元数据等地址
- 结合状态码、错误文本、时延做侧信道探测

**已有缓解：**

- 仅 HTTPS，且端口为空或 443
- 禁止 URL userinfo
- 字面量 `localhost` / `.local` / `.internal` 拒绝
- 私网、回环、链路本地、组播及部分特殊前缀 blocklist
- 外部图片请求**不携带** SSO / Cloudflare Cookie

**仍不足：** 校验与拨号分离，无 IP pin、无连接前二次校验。

**修复建议：**

1. 校验阶段记录 IP 列表，`DialContext` 强制使用已校验 IP（或自定义 `http.Transport.DialContext`）。
2. 连接前再次 resolve，与白名单 IP 集合不一致则中止。
3. 出站层统一拦截 RFC1918 / ULA / 元数据地址，不依赖应用层单点校验。
4. 对重定向：当前 browser/build client 多处 `NotFollowRedirects`；若未来跟随重定向，必须对 Location 重跑同一套策略。

---

### 3.3 [高] 默认依赖第三方 Statsig 签名服务

**位置：**

- `internal/infra/config/config.go`：`DefaultStatsigSignerURL = "https://grok.wodf.de/sign"`
- `internal/infra/provider/web/statsig.go`

**机制：**

1. 使用账号 SSO Cookie 拉取 Grok 首页 HTML，提取 `metaContent`。
2. 将 `method` / `path` / `metaContent` POST 到签名服务，获取 `x-statsig-id`。
3. 默认签名端点为第三方域名。

**风险：**

| 类型 | 说明 |
| --- | --- |
| 供应链可用性 | 第三方故障或改接口 → Web Provider 大面积失败 |
| 隐私 / 合规 | meta 与请求路径交给外部；meta 抓取阶段持有 SSO |
| 运营不可控 | 默认“黑盒”签名站，难审计 |

**已有缓解：**

- `signerurl.Validate` 限制签名 URL 形态（公网 HTTPS:443 或显式内网）
- 签名客户端 `CheckRedirect = ErrUseLastResponse`
- 响应体大小上限、字段格式校验

**修复建议：**

1. 生产配置**强制**显式 `statsigSignerURL`，拒绝隐式默认外域（或启动告警）。
2. 文档与管理端 UI 标明“外部签名 = 信任边界外移”。
3. 优先内嵌 / 自建签名实现；外置服务需 mTLS 或共享密钥。
4. 审计日志记录 signer 主机名（不含 meta 正文）。

---

### 3.4 [中高] 媒体资源完全公开、长期缓存

**位置：**

- `internal/transport/http/server.go`：`mediaHandler.RegisterPublic(router)`（在 ClientAuth 之外）
- `internal/transport/http/media/handler.go`
- `internal/application/media/service.go`：`newAssetID` / `PublicImageURL`

**机制：**

- 路径：`GET|HEAD /v1/media/images/:assetId`
- ID：`img_` + 24 字节 CSPRNG（Base64URL）≈ 192 bit 熵，**不可枚举**
- 响应：`Cache-Control: public, max-age=31536000, immutable`

**风险：**

- URL 一旦出现在 API 响应、日志、Referer、聊天记录、CDN，**任意持有者可匿名下载**
- 一年缓存放大泄露窗口
- 无 per-object ACL、无过期签名

**做得好的点：**

- 本地存储路径严格防穿越（`LocalStore.resolve` + 单测）
- 原子硬链接提交，避免半成品
- 内容类型嗅探 + 白名单 MIME

**修复建议：**

1. 默认改为 HMAC 签名 URL（`exp` + `sig`），短 TTL。
2. 或要求 Client Key / 会话 cookie 访问。
3. 管理端提供“公开 / 私有”策略开关。
4. 敏感场景禁用 `public` 长缓存。

---

### 3.5 [中] 管理员会话：Access 与 Refresh 生命周期不对称

**位置：** `internal/application/adminauth/service.go`、`internal/infra/persistence/relational/admin_repository.go`

**机制：**

- Access：JWT HS256，含 `adminId` + `sessionId`，默认 TTL 15m
- Refresh：不透明随机串，仅存 SHA-256；轮换时 CAS 更新 hash
- `AuthenticateAccess`：验 JWT 后查 session 行是否存在且未过期
- Logout / 改密：删除 session 行 → Access 立即失效

**问题：**

1. **Refresh 轮换不删除 session 行**，只改 `refresh_token_hash` → 旧 Access 在剩余 TTL 内仍可用。
2. 无 Access 吊销列表；无法对单 token 即时踢出（只能删 session）。
3. `auth.secureCookies` 默认 `false`：TLS 终结在反代时，若未正确配置，Refresh Cookie 可能被明文传输。
4. 登录限流键对 Admin 使用 `RemoteAddr`，对反代后所有客户端可能合并为一个 IP。

**修复建议：**

1. 轮换 Refresh 时递增 `session_version`，JWT 携带 version，校验时比对。
2. 生产强制 `secureCookies: true`（或检测 `X-Forwarded-Proto=https` 时自动 Secure）。
3. 配置 `TrustedProxies`，限流与审计 IP 使用可信链。
4. 可选：短 Access TTL（5m）+ 滑动刷新。

---

### 3.6 [中] Client API Key：裸 SHA-256 + 可逆密文

**位置：**

- `internal/infra/security/token.go`：`HashToken` = SHA-256(hex)
- `internal/application/clientkey/service.go`：创建时写 `SecretHash` + `EncryptedSecret`
- `RevealSecret`：管理员可解密回明文

**分析：**

- 密钥熵高（`g2a_<12 hex>_<~32B opaque>`），在线暴力不现实。
- DB 泄露时：无 salt/pepper 的 SHA-256 对**高熵 secret** 仍难破解，但与行业最佳实践（argon2id）不符。
- **更大风险**是同库 `EncryptedSecret`：一旦 `credentialEncryptionKey` 一并泄露，所有 Key 明文可批量解密。
- 进程内 auth cache TTL 1s：禁用/改权限有短暂延迟；带 Billing Limit 的 Key 不进缓存（有意设计）。

**修复建议：**

1. 鉴权哈希升级 argon2id / scrypt，保留版本前缀便于迁移。
2. 加密使用 AAD（绑定 `key_id` / `prefix`），防密文挪移。
3. Reveal 操作二次确认 + 审计告警；可选“创建后不可再揭示”。
4. 加密密钥与 JWT 密钥分管、支持版本轮换。

---

### 3.7 [中] AES-GCM 无 AAD、无密钥版本

**位置：** `internal/infra/security/cipher.go`

**问题：**

- `Seal` / `Open` 的 additionalData 为 `nil`
- 拥有 DB 写权限的攻击者可将账号 A 的密文复制到账号 B，解密仍成功
- 无 key id → 轮换 `credentialEncryptionKey` 只能导致旧数据全部不可用

**修复建议：**

- AAD = `provider | account_id | field_name`（或 client key id）
- 密文前缀增加 `enc_v1:` 与 key version
- 后台提供 re-encrypt 任务

---

### 3.8 [中] 未配置 Trusted Proxies，IP 语义混乱

**位置：** Gin 默认行为；`c.ClientIP()` 用于审计 / 访问日志 / Prompt Cache 指纹；Admin 登录限流用 `RemoteAddr`

**全仓未发现** `SetTrustedProxies` / `RemoteIPHeaders` 配置。

| 场景 | 后果 |
| --- | --- |
| 直连公网 | `ClientIP` 可能被客户端伪造 `X-Forwarded-For` |
| 反代后未信任 | 限流全部落在反代 IP；真实客户端无法区分 |
| Prompt Cache 指纹 | 伪造 IP 可污染亲和映射 |

**修复建议：**

```yaml
# 示意：配置可信反代 CIDR，再启用转发头
server:
  trustedProxies: ["10.0.0.0/8", "172.16.0.0/12"]
```

代码侧：`engine.SetTrustedProxies(...)`，文档写清“仅信任最内层反代”。

---

### 3.9 [中] 凭据导出爆炸半径过大

**位置：** `internal/application/account/service.go` → `ExportCredentials`

- 最多一次导出 10000 个 Build 账号的 access/refresh **明文**
- 仅依赖 Admin 鉴权
- Admin 失陷 ≈ 整个上游账号池被拖走

**修复建议：**

1. 导出需二次密码 / TOTP / 确认码。
2. 强制写高优先级审计事件 + 可选 Webhook 告警。
3. 分页 / 限流 / 冷却时间。
4. 默认导出脱敏（仅 metadata），明文导出需独立权限位。

---

### 3.10 [低] 浏览器安全头不完整

**位置：** `internal/transport/http/middleware/request.go` → `SecurityHeaders`

**已设置：** `X-Content-Type-Options`、`X-Frame-Options`、`Referrer-Policy`、`Permissions-Policy`  
**缺失：** CSP、HSTS、`Cross-Origin-Opener-Policy`、`Cross-Origin-Resource-Policy` 等

同源托管 Admin SPA 时，XSS 影响面更大。

---

### 3.11 [低] 管理员密码策略偏弱

- 最短 8 字符，无复杂度要求
- bcrypt `DefaultCost`（可接受）
- 登录限流：每分钟固定窗 30/IP、12/user，无账户锁定告警、无验证码

---

### 3.12 [信息] 公开探测面

| 端点 | 说明 |
| --- | --- |
| `/healthz` | 存活 |
| `/readyz` | 就绪及启动恢复统计（凭证刷新计数等，无内部错误正文） |
| `/swagger/*` | 配置开启时暴露完整 API 契约 |
| Docker `0.0.0.0:8000` | 默认监听全接口，需依赖网络隔离 |

---

## 4. 逻辑与设计问题

### 4.1 `retryStatusCodes` 配置基本无效

**配置：** `routing.retryStatusCodes`、`routing.retryServerErrors`（见 `config.example.yaml`）  
**实现：** `gateway/service.go` 中硬编码：

```go
func isRetryable(status int) bool {
    return status == 402 || status == 403 || status == 429 || status >= 500
}
```

**后果：**

- 管理端 / YAML 修改重试策略不生效
- 所有 5xx（含 501/505 等）均触发换号重试
- 与文档/示例配置不一致，运维误判

**建议：** `isRetryable` 读取热更新配置；默认 5xx 仅 500/502/503/504。

---

### 4.2 403 默认可重试导致账号池连坐

403 进入可重试集合后会换账号。失败分类依赖上游 body 文本启发式（`gateway/failure.go`）。

上游文案变更时可能：

- 永久封禁被当成临时失败反复打击
- 额度耗尽未识别 → 错误冷却 / 未冷却

Web 出口反爬 403 有特殊分支（不误伤账号），其它 Provider 仍粗糙。

**建议：** 403 默认**不重试**，仅在分类为 `AccountScoped` 且非永久拒绝时换号；未知 403 快速失败并告警。

---

### 4.3 选号候选缓存 2 秒陈旧

**位置：** `gateway/selector.go` → `candidateCacheTTL = 2 * time.Second`

刚被禁用、额度耗尽、进入冷却的账号，在 TTL 内仍可能被选中 → 多余失败与重试。

**建议：** 状态变更（冷却、禁用、额度）时主动 `invalidate` 对应 cache key。

---

### 4.4 RPM 为固定分钟窗，非滑动窗

- Memory：`startedAt` 起 60s 内累计
- Redis：`INCR` + 首次 `PEXPIRE 60000`

窗口边界可出现约 **2×RPM** 突发。

**建议：** 滑动日志 / 令牌桶；至少文档标明语义。

---

### 4.5 计费预留与 Finalize 竞态

流程：`ReserveBilling` → 上游调用 → `Finalize` 写审计并处理预留。

| 场景 | 风险 |
| --- | --- |
| 客户端断流 / 进程被杀 | 预留占用至 TTL（文本 2h / 媒体 24h） |
| 估算 vs 实际上游 usage | Billing Limit 过严或过松 |
| 清理任务延迟 | 高压下短暂“假额度用尽” |

**建议：** 缩短 TTL、成功路径尽快 settle、失败必 cancel；估算加安全边际系数可配置。

---

### 4.6 粘滞会话与失败排除的张力

单次请求失败会将 `accountID` 加入 `excluded`，但 sticky 可能在**后续请求**仍钉回该账号，直到冷却 / TTL 处理完成。

有 `previous_response_id` 时 `attempts` 强制为 1：符合会话归属，但账号故障时无法 failover——需在文档中明确。

---

### 4.7 Bootstrap 非原子

```go
count, _ := admins.Count(ctx)
if count > 0 { return nil }
admins.Create(...)
```

双实例同时首次启动可能竞态。应使用 DB 唯一约束 + 事务 / `INSERT ... WHERE NOT EXISTS`。

---

### 4.8 Token 估算计费不精确

`pkg/tokencount` 为启发式估算。`UsageSourceEstimated` 路径会影响 Billing Limit，可能误杀合法流量或放行超额。

---

### 4.9 请求体上限与图片附件上限不一致

- 服务端 `maxBodyBytes` 默认 32 MiB
- 对话图片合计上限 64 MiB（`attachments.go`）

大图可能在网关先被拒，错误信息不友好。应对齐限制或分层校验提示。

---

### 4.10 Client Key 空模型列表 = 全部模型

`CanUseModel`：`AllowedModels` 为空表示不限制。符合常见 API 网关习惯，但管理端若 UI 误传空数组，会**意外放开**全部模型。

建议：区分 `null`（全部）与 `[]`（无权限），或创建时强制至少选一个。

---

## 5. 攻击链（研究视角）

以下描述用于理解风险，**不提供可直接利用的 exploit 步骤**。

### 5.1 链 A：内网探测（依赖 DNS 重绑定）

1. 合法 Client Key 发起带远程图片 URL 的对话
2. 域名第一次解析公网 IP，通过 `validateRemoteImageURL`
3. 第二次解析指向内网 / 元数据
4. 结合错误与时延推断可达性

### 5.2 链 B：Admin 失陷拖库

1. 弱 bootstrap 密码、未改密、或 Cookie 明文被中间人获取
2. 登录管理端
3. `ExportCredentials` + Client Key `RevealSecret`
4. 上游 OAuth/SSO 与下游 Key 外流

### 5.3 链 C：数据库 + 加密密钥同时泄露

1. 获取 SQLite/PostgreSQL
2. 获取 `credentialEncryptionKey`
3. 批量解密账号凭据与 Client Key 明文副本

### 5.4 链 D：媒体 URL 泄露

1. 图片 URL 出现在日志 / 第三方 / 转发消息
2. 匿名长期拉取

---

## 6. 正面实践清单

| 项 | 说明 |
| --- | --- |
| 媒体路径防穿越 | `filepath.Rel` 边界检查 + 单测 |
| 远程图基础 SSRF 策略 | HTTPS-only、禁 userinfo、私网 blocklist |
| SSO→Build 转换 | `safeXAIURL` 限制 `*.x.ai`，重定向最多 8 次 |
| Refresh 轮换 | CAS `expectedTokenHash`，防重放 |
| 登录假用户 | dummy bcrypt |
| 审计脱敏 | 正则剥离常见 secret 形态 |
| Docker | non-root 用户、`no-new-privileges` |
| 配置校验 | 拒绝示例占位 `jwtSecret` / 密码；加密密钥长度校验 |
| 并发租约 | Memory/Redis 均可释放；Redis 带 lease token |

---

## 7. 修复优先级

### P0（尽快）

1. 图片下载 IP pin / 二次 DNS 校验，出站统一拦私网  
2. Statsig 签名：去掉不安全默认或启动强制显式配置  
3. 生产 `secureCookies=true` + Trusted Proxies  

### P1

1. 媒体签名 URL 或鉴权访问  
2. 凭据导出二次确认 + 强审计  
3. `isRetryable` 接入配置；收紧 403/5xx 策略  
4. Client Key 哈希升级 + 加密 AAD  

### P2

1. 滑动窗口 RPM  
2. 选号缓存主动失效  
3. CSP / HSTS  
4. 密码策略与登录告警  
5. `/readyz` 对外降敏（可选开关）  

### P3

1. 计费预留与真实 usage 对齐  
2. Bootstrap 原子化  
3. 模型白名单空数组语义澄清  
4. 文档与配置项一致性审查  

---

## 8. 验证建议（加固后）

```bash
# 单元 / 竞态
cd backend && go test ./...
cd backend && go test -race ./...

# 重点包
go test ./internal/infra/provider/web/ -count=1
go test ./internal/application/gateway/ -count=1
go test ./internal/application/clientkey/ -count=1
go test ./internal/infra/security/ -count=1
go test ./internal/infra/media/ -count=1
```

**手工检查清单：**

- [ ] 远程图片 URL 指向解析到私网的域名应失败  
- [ ] DNS 在校验后变更 IP 应失败（若实现 pin）  
- [ ] 禁用 Client Key 后 ≤1s 内拒绝（或文档标明缓存窗口）  
- [ ] 修改 `retryStatusCodes` 后行为与配置一致  
- [ ] 导出凭据产生审计记录  
- [ ] HTTPS 部署下 Refresh Cookie 带 `Secure`  
- [ ] 反代后限流按真实客户端 IP 生效  

---

## 9. 相关代码索引

| 主题 | 路径 |
| --- | --- |
| 路由与鉴权边界 | `internal/transport/http/server.go` |
| Admin / Client 中间件 | `internal/transport/http/middleware/auth.go` |
| 管理员登录会话 | `internal/application/adminauth/service.go` |
| Client Key | `internal/application/clientkey/service.go` |
| 凭据加解密 | `internal/infra/security/cipher.go` |
| 远程图片 / SSRF | `internal/infra/provider/web/attachments.go` |
| Statsig 签名 | `internal/infra/provider/web/statsig.go` |
| 签名 URL 策略 | `internal/pkg/signerurl/policy.go` |
| 选号 | `internal/application/gateway/selector.go` |
| 重试与计费 | `internal/application/gateway/service.go` |
| 失败分类 | `internal/application/gateway/failure.go` |
| 公开媒体 | `internal/transport/http/media/handler.go` |
| 本地媒体存储 | `internal/infra/media/local_store.go` |
| 配置校验 | `internal/infra/config/config.go` |
| 示例配置 | `config.example.yaml` |

---

## 10. 结论

Grok2API 不是粗糙的反向代理，而是具备账号调度、审计脱敏、凭据加密与基础 SSRF 意识的生产向网关。主要短板集中在：

1. **SSRF 校验与真实拨号的 DNS 时间差**  
2. **默认第三方 Statsig 签名与凭据导出的爆炸半径**  
3. **公开媒体 URL 与部署默认值（Cookie Secure、反代 IP）**  
4. **重试 / 限流 / 缓存的配置与实现脱节**

优先完成 P0/P1 后，整体安全姿态可达到与其“多账号凭据网关”角色相匹配的水平。

---

## 修订记录

| 日期 | 说明 |
| --- | --- |
| 2026-07-16 | 初版：基于 `grok2api-push` 静态代码审计 |
| 2026-07-16 | 加固落地：远程图 DNS pin、禁止第三方默认 Statsig、TrustedProxies、`isRetryable` 接配置且默认不含 403、Client Key 加密 AAD（兼容旧密文）、安全响应头 HSTS/COOP/CORP |
