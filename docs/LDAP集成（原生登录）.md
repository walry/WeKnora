# LDAP 集成（线路 B：原生登录）实施指导

> 本文档面向执行实现的 AI Agent / 开发者。
> 目标：在 WeKnora 中实现**原生 LDAP 登录**，配置由系统管理员在设置页维护、落库到 `system_settings`，登录时实时读库认证。
> 文档基于对当前代码的实测（行号以出稿时为准，改动前请重新核对，允许 ±5 行漂移）。

---

## 0. 给执行者的约定

1. **本方案是“线路 B”**。仓库里可能已经存在“线路 A”（Dex 桥接 OIDC）的改动：`misc/dex-config.yaml`、`docker-compose*.yml` 里的 dex 服务、`docs/OIDC认证调用流程.md`。**不要删除、不要重构这些文件**，两条路线并存。
2. **配置不读 env**（唯一例外见 3.3 `SYSTEM_AES_KEY`）。不要新增 `LDAP_*` 环境变量，也不要往 `config/config.yaml` 里加 `ldap_auth` 段。
3. **按第 5 节的阶段顺序实施**，每个阶段结束时 `go build ./...` 必须通过。不要一次性铺开所有改动。
4. 涉及现有行为的改动（`system_settings` 的 Update/List/Get/audit），**必须先补测试再改逻辑**，这些路径正在服务全平台的运行时开关。
5. 任何日志、审计、错误返回里**不得出现 `bind_password` 明文或密文**。
6. 失败一律 fail-closed：配置读不到 / 解密失败 / LDAP 不可达 → 拒绝登录，不要退化成“跳过认证”。

---

## 1. 目标与非目标

### 目标

- 超管在「系统设置 → 认证」页面填写 LDAP 参数，保存到数据库，无需重启、无需改 env。
- 登录页出现「LDAP 登录」入口，输入域账号 + 密码直接登录。
- 首次登录自动开本地账号（复用现有 JWT / 租户 / 成员体系）。
- 配置可「测试连接」，分阶段返回失败原因。
- 密码加密落库，接口返回脱敏，审计脱敏。

### 非目标（本期不做，不要顺手实现）

- LDAP 组 → 租户角色映射（二期）。
- 定时同步 LDAP 用户/禁用（二期）。
- 出站代理、连接池复用、多 LDAP 服务器 failover。
- 把 OIDC 配置也迁到数据库（两种认证同时改容易互相带崩）。
- 修改 `/auth/login` 本地登录的限流策略。

---

## 2. 现状事实（已核对，实施时作为基准）

| 能力 | 位置 | 说明 |
|---|---|---|
| 本地账号登录 | `internal/handler/auth.go:179` → `internal/application/service/user.go:159` | 邮箱 + bcrypt |
| OIDC 登录 | `internal/application/service/user.go:401` | 配置只来自 `cfg.OIDCAuth`，**不读 DB** |
| 首次开户 | `internal/application/service/user.go:1161` `provisionOIDCUser` | 随机 32 位密码 + `Register` + 建个人空间 |
| `Register` | `internal/application/service/user.go:85` | 内部会 `CreateTenant` + `EnsureOwner`，天然建个人空间 |
| 租户解析 / 发 token | `user.go:636` `resolveLoginTenantID`、`generateTokensForTenant` | 登录后统一走这两步 |
| 系统设置表 | `internal/types/system_setting.go:25`，migration `000053` | 平台级（非租户级），`system_settings` |
| 设置注册表 | `internal/application/service/system_setting.go:101` `registry` | 只允许表内 key，UI 无法注入任意 key |
| 设置读取 | `system_setting.go:453 resolveRaw`、`:487/514/532/561` GetInt/GetString/GetBool/GetStringList | 优先级 DB > env > 默认，带内存 cache |
| 设置写入 | `system_setting.go:772 Update` | 校验类型 → Upsert → 更新 cache → Redis pubsub → 审计 |
| 审计 | `system_setting.go:1028 emitChangeAudit` | **原样写入 old_value / new_value**，需脱敏 |
| 管理接口 | `internal/handler/system.go:1348/1424/1468/1571`；路由 `internal/router/router.go:810-813` | 挂在 `SystemAdmin()` 组下 |
| 登录页探测 | `internal/handler/auth.go:659 GetAuthConfig`（`/auth/config`） | 前端启动时读取，用来决定显示哪些登录方式 |
| 公开路径白名单 | `internal/middleware/auth.go:22 noAuthAPI` | 漏加会被 JWT 中间件拦成 401 |
| 公开接口限流 | `middleware.PublicAuthRateLimit()`，用法见 `router.go:700` | `/auth/login` 当前**没有**挂，新接口要挂 |
| 加密设施 | `internal/utils/crypto.go:19 GetAESKey`、`:26 EncryptAESGCM`、`DecryptStoredSecret` | `SYSTEM_AES_KEY` 必须是 32 字节 |
| 加密先例 | `internal/types/tenant.go:152`、`types/web_search_provider.go:92` | `BeforeSave` 里加密、读取时解密 |
| 用户模型 | `internal/types/user.go:78` | `password_hash NOT NULL`，`email` 唯一，**无来源字段** |
| 迁移 | `migrations/versioned/` 最新 `000064`；`migrations/sqlite/000000_init.up.sql:221` | 新迁移从 `000065` 开始 |
| 前端设置页 | `frontend/src/views/system/SystemSettings.vue` | 平铺列表，按 `value_type` 分支渲染（`:225/231/242`），无 json 分支 |
| 前端登录页 | `frontend/src/views/auth/Login.vue:212` | OIDC 按钮是 `v-if="oidcEnabled"`，可参照 |

---

## 3. 架构决策（已定，不要重新讨论）

### 3.1 配置存单键 JSON，不拆成多个 key

`system_settings` 一行：

```
key         = "auth.ldap"
value_type  = "json"
category    = "auth"
value       = { ...见 3.2... }
is_secret   = false        ← 不要设 true，否则整行在列表里只剩掩码，看不到 server_url
```

理由：

- 10 个字段拆成 10 个 key = 10 次 PUT，中途失败会留下半套配置（`server_url` 换了、`base_dn` 没换）。
- 跨字段校验（`enabled=true` 时必填项）没有落点。
- 字段级脱敏比整行脱敏更合适：`server_url` 要展示，`bind_password` 要隐藏。

代价：`system_settings` 目前只支持 `int`/`string`/`bool`/`string_list`，需要新增 `json` 类型（第 5.1 节）。

### 3.2 密码字段加密后落库

- 保存时用 `utils.EncryptAESGCM(bindPassword, utils.GetAESKey())`，得到 `enc:v1:...`。
- **必须先检查 `GetAESKey() != nil`**。`EncryptAESGCM` 在 key 为 nil 时会**静默返回明文**（`crypto.go:27`），不检查就会“以为加密了其实明文入库”。key 缺失 → 返回 400，提示配置 `SYSTEM_AES_KEY`。
- 读取时用 `utils.DecryptStoredSecret`（严格版）。解密失败（key 轮换）→ 记 error 日志 + 拒绝登录，提示管理员重新录入密码。
- `SYSTEM_AES_KEY` 是**唯一保留的 env 依赖**，因为密钥不能和密文存在同一个库。

### 3.3 登录路径绕过缓存，直查数据库

`systemSettingService` 有内存 cache + Redis pubsub 失效（`:264 preload`、`:411 publishChange`）。但：

- 无 Redis 的多副本部署里，各副本 cache 各自为政：超管关掉 LDAP 后，另一个副本仍能登录。
- `GetXxx` 在 DB 出错时会静默退到 env/默认（`:487` 起），认证路径上不能接受这种降级。

因此新增 `GetJSONFresh(ctx, key)`，**直查主键单行、不读 cache、不做 env 回退**，只给登录和测试连接用。登录 QPS 很低，一次主键查询可忽略。管理端 `List`/`Get` 继续走 cache。

### 3.4 新增 `users.auth_source`

取值 `local` / `oidc` / `ldap`。用途：

- 本地 `Login` 对非 `local` 用户拒绝密码登录；
- `ChangePassword`（`internal/handler/auth.go:604`）对非 `local` 用户拒绝，提示“该账号由 LDAP 管理”；
- 后续做同步/审计时分得清账号来源。

`password_hash` 是 `NOT NULL`，LDAP 用户沿用 OIDC 的随机密码做法，不要改列约束。

### 3.5 租户归属

`default_tenant_id = 0` → 每个 LDAP 用户首次登录自动建个人空间（`Register` 已内置，行为与现在注册一致）。
`default_tenant_id > 0` → 开户时额外 `memberService.AddMember(ctx, user.ID, tenantID, types.TenantRoleViewer, nil)` 并把 `user.TenantID` 指向该租户。默认给 `viewer`，不要给 `owner`（否则每个 LDAP 用户都能管理共享空间）。

---

## 4. 数据契约

### 4.1 `auth.ldap` 的 JSON 结构

```json
{
  "enabled": false,
  "server_url": "ldap://192.168.1.100:3890",
  "start_tls": false,
  "skip_verify": false,
  "bind_dn": "uid=binduser,ou=people,dc=univista-isg,dc=com",
  "bind_password": "enc:v1:....",
  "base_dn": "ou=people,dc=univista-isg,dc=com",
  "user_filter": "(objectClass=person)",
  "username_attr": "uid",
  "email_attr": "mail",
  "name_attr": "displayName",
  "default_tenant_id": 0,
  "timeout_seconds": 5
}
```

| 字段 | 类型 | 默认 | 规则 |
|---|---|---|---|
| `enabled` | bool | `false` | 总开关。为 false 时登录接口直接拒绝、前端不显示入口 |
| `server_url` | string | `""` | 只允许 `ldap://host:port` 或 `ldaps://host:port`。**禁止 `http://`**（会被当成 host，报 `too many colons in address`）；**禁止省略端口** |
| `start_tls` | bool | `false` | 仅 `ldap://` 有意义；`true` 时连接后发送 StartTLS。lldap 3890 **不支持** StartTLS |
| `skip_verify` | bool | `false` | 仅 TLS 场景（`ldaps://` 或 `start_tls`）生效；`true` 只允许内网自签调试 |
| `bind_dn` | string | `""` | 服务账号完整 DN，**必须包含 `ou=people`**（lldap 固定格式）；`enabled=true` 时必填 |
| `bind_password` | string | `""` | 落库为 `enc:v1:` 密文；API 返回时**整字段删除**，用 `bind_password_set` 代替 |
| `base_dn` | string | `""` | 用户搜索根。lldap 是 `ou=people,dc=...`；`enabled=true` 时必填 |
| `user_filter` | string | `(objectClass=person)` | 必须成对括号；留空等价于不加条件（不建议） |
| `username_attr` | string | `uid` | AD 用 `sAMAccountName`，lldap 用 `uid` |
| `email_attr` | string | `mail` | 为空则无法映射本地账号，必须校验非空 |
| `name_attr` | string | `displayName` | 仅用于展示 |
| `default_tenant_id` | int | `0` | 0 = 每人个人空间 |
| `timeout_seconds` | int | `5` | 限制 1–30，作用于 dial + 每次 bind/search |

### 4.2 统一常量

```go
// internal/application/service/ldap_auth.go
const ldapSettingKey = "auth.ldap"
```

不要在 handler、service、前端三处各写一遍字符串字面量。

### 4.3 错误语义

| 场景 | 对客户端 | 服务端日志 |
|---|---|---|
| LDAP 未启用 | 401 `{"success":false,"message":"Invalid username or password"}` | info（不记用户名密码） |
| 用户搜索 0 条 | 401，同上（不区分“用户不存在/密码错”） | info + sanitized username |
| 用户搜索 >1 条 | 401，同上 | **error**，带 filter（不含密码） |
| 用户 bind 失败 | 401，同上 | info |
| 账号被本地禁用 | 403 `Account is disabled` | warn |
| 配置缺失 / 解密失败 / 连接失败 | 503 `LDAP authentication is temporarily unavailable` | error，带阶段名 |

统一错误文案是为了防止账号枚举；详细原因只进日志。

---

## 5. 实施阶段

### 5.1 P0：给 `system_settings` 增加 `json` 类型

只改 `internal/application/service/system_setting.go` 与类型定义，不碰 LDAP。

**(a) `settingSpec`（`:61`）增加两个可选钩子**

```go
type settingSpec struct {
    // ...existing fields...

    // Prepare 在 encodeForType 之前调用，用于把“前端提交的增量”合并到
    // 已有值上（典型场景：密钥字段留空表示不修改）。prev 为 nil 表示首次保存。
    Prepare func(prev types.JSON, incoming any) (any, error)

    // Redact 用于在写审计、返回 List/Get 之前脱敏。nil 表示无需脱敏。
    Redact func(types.JSON) types.JSON
}
```

**(b) `encodeDefault`（`:291`）增加 `case "json"`**

```go
case "json":
    if spec.Default == nil {
        return types.JSON(`{}`), nil
    }
    b, err := json.Marshal(spec.Default)
    if err != nil {
        return nil, err
    }
    return types.JSON(b), nil
```

**(c) `encodeForType`（`:1059`）增加 `case "json"`**

```go
case "json":
    b, err := json.Marshal(rawValue)
    if err != nil {
        return nil, err
    }
    if !json.Valid(b) { // 理论不可达，防御性
        return nil, errors.New("invalid json value")
    }
    return types.JSON(b), nil
```

**(d) `resolveRaw` / Get*：不动**

`json` 类型不走 `GetString` 等现有方法，新增 `GetJSON` / `GetJSONFresh`（见 5.2）。

**(e) 回归测试**

`internal/application/service/system_setting_test.go`（若不存在则新建）至少覆盖：

- `Update("auth.ldap", map[string]any{...})` 后 `Get` 返回等价 JSON；
- 未知 key 仍 400；
- `encodeDefault` 对 `json` 且 `Default == nil` 返回 `{}`；
- `List` 中 `auth.ldap` 虚拟行不 panic（走 `fallbackJSONForSpec`）。

> 注意：`fallbackJSONForSpec`（`:682`）会先看 `spec.EnvName`，`auth.ldap` 的 `EnvName` 必须是 `""`，保证不会意外从 env 读。

### 5.2 P1：配置类型 + 读取接口 + 加解密 + 脱敏

**(a) 新增 `internal/types/ldap.go`**

```go
package types

type LDAPAuthConfig struct {
    Enabled         bool   `json:"enabled"`
    ServerURL       string `json:"server_url"`
    StartTLS        bool   `json:"start_tls"`
    SkipVerify      bool   `json:"skip_verify"`
    BindDN          string `json:"bind_dn"`
    BindPassword    string `json:"bind_password,omitempty"`
    BaseDN          string `json:"base_dn"`
    UserFilter      string `json:"user_filter"`
    UsernameAttr    string `json:"username_attr"`
    EmailAttr       string `json:"email_attr"`
    NameAttr        string `json:"name_attr"`
    DefaultTenantID uint64 `json:"default_tenant_id"`
    TimeoutSeconds  int    `json:"timeout_seconds"`

    // 仅出参，不入库
    BindPasswordSet bool `json:"bind_password_set,omitempty"`
}
```

方法：

- `Normalize()`：trim 所有字符串、`ServerURL` 去掉末尾 `/`、空 `user_filter` 补默认、空 attr 补默认、`TimeoutSeconds <= 0` → 5。
- `Validate() error`：`enabled=false` 时只校验 `server_url` 若有值必须是合法 scheme；`enabled=true` 时校验必填、`user_filter` 括号配对、`timeout_seconds` 范围。
- `Masked() LDAPAuthConfig`：返回副本，`BindPassword=""`，`BindPasswordSet = original.BindPassword != ""`。
- `EncryptSecret(key []byte) error` / `DecryptSecret() error`：调用 `utils.EncryptAESGCM` / `utils.DecryptStoredSecret`。

**(b) `internal/types/interfaces/system_setting.go:42` 增加**

```go
// GetJSON 走缓存，用于管理端展示。
GetJSON(ctx context.Context, key string) (types.JSON, bool)
// GetJSONFresh 直查数据库主键单行，不读缓存、不回退 env。用于认证等安全敏感路径。
GetJSONFresh(ctx context.Context, key string) (types.JSON, bool)
```

`bool` 表示“DB 中确实存在该行”。错误一律 log + `false`（与现有 Get* 的降级策略一致），但认证路径必须把 `false` 当作“未配置”。

**(c) `system_setting.go` 实现两个方法**

```go
func (s *systemSettingService) GetJSONFresh(ctx, key) (types.JSON, bool) {
    row, err := s.repo.Get(ctx, key)
    if err != nil || row == nil {
        return nil, false
    }
    return row.Value, true
}
```

**(d) 注册 `auth.ldap`（`:101` 的 registry）**

```go
"auth.ldap": {
    Type:     "json",
    EnvName:  "", // 明确不回退 env
    Default:  map[string]any{"enabled": false},
    Category: "auth",
    Description: "LDAP 登录配置。含服务账号密码，密码加密存储、接口回显脱敏。" +
        "修改后立即生效，无需重启。",
    Prepare:  prepareLDAPConfig,   // 见 (e)
    Redact:   redactLDAPConfig,    // 见 (f)
},
```

**(e) `Prepare`：合并“留空表示不修改”**

```go
func prepareLDAPConfig(prev types.JSON, incoming any) (any, error) {
    m, ok := incoming.(map[string]any)
    if !ok {
        return nil, errors.New("ldap config must be an object")
    }
    if pw, _ := m["bind_password"].(string); pw == "" {
        // 未提交新密码：沿用旧密文
        if prev != nil {
            var old types.LDAPAuthConfig
            if err := json.Unmarshal(prev, &old); err == nil && old.BindPassword != "" {
                m["bind_password"] = old.BindPassword
            }
        }
    }
    return m, nil
}
```

> `Update` 当前在 `:819` 才取 `prev`，需要把取 `prev` 的代码上移到 `encodeForType` 之前，并把 `prev.Value` 传给 `Prepare`。重排时保持“审计用 prev 是 DB 实时值”的原有语义（不要改成从 cache 取）。

**(f) `Redact`：审计/List/Get 脱敏**

```go
func redactLDAPConfig(raw types.JSON) types.JSON {
    var cfg types.LDAPAuthConfig
    if err := json.Unmarshal(raw, &cfg); err != nil {
        return raw
    }
    masked := cfg.Masked()
    b, _ := json.Marshal(masked)
    return types.JSON(b)
}
```

接入三处：

1. `Update` 内 `emitChangeAudit(ctx, key, spec.Type, spec.Redact(oldValue), spec.Redact(encoded))`；
2. `List` 返回前对每个 row 应用 `spec.Redact`；
3. `Get` 返回前同上。

**注意顺序**：加密必须在 `encodeForType` 之前完成（即 `Update` 里先把 `bind_password` 明文换成 `enc:v1:`，再 marshal）。这样 `Redact` 拿到的是密文，脱敏后审计里连密文都没有。实现位置建议在 `Prepare` 之后、`encodeForType` 之前加一步 `spec.Seal`（或者直接在 `prepareLDAPConfig` 末尾做加密，二者选一，不要两处都做）。**如果 `GetAESKey()` 为 nil，`prepareLDAPConfig` 返回错误**，让 `Update` 以 400 结束。

### 5.3 P2：LDAP 客户端与配置加载

**(a) 依赖**

```bash
go get github.com/go-ldap/ldap/v3
```

**(b) 新增 `internal/application/service/ldap_auth.go`**

```go
type LDAPConfigLoader struct {
    settings interfaces.SystemSettingService
}

var (
    ErrLDAPDisabled          = errors.New("ldap login is disabled")
    ErrLDAPConfigInvalid     = errors.New("ldap configuration is invalid")
    ErrLDAPSecretUnavailable = errors.New("ldap bind secret unavailable")
)

// Load 用 GetJSONFresh 直查 DB；enabled=false 返回 ErrLDAPDisabled。
// 解密失败返回 ErrLDAPSecretUnavailable（不要吞掉，也不要拿密文当密码）。
func (l *LDAPConfigLoader) Load(ctx context.Context) (*types.LDAPAuthConfig, error)
```

加载流程：`GetJSONFresh` → 未找到/`enabled=false` → `ErrLDAPDisabled` → `Unmarshal` → `Normalize` → `DecryptSecret`（严格版，失败即返回）→ `Validate`。

**(c) 连接与认证**

```go
// dial 用 DialURL，超时同时作用于 TCP 和后续操作
func dialLDAP(cfg *types.LDAPAuthConfig) (*ldap.Conn, error) {
    opts := []ldap.DialOpt{
        ldap.DialWithDialer(&net.Dialer{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second}),
    }
    if cfg.SkipVerify {
        opts = append(opts, ldap.DialWithTLSConfig(&tls.Config{InsecureSkipVerify: true}))
    }
    conn, err := ldap.DialURL(cfg.ServerURL, opts...)
    if err != nil {
        return nil, fmt.Errorf("dial: %w", err)
    }
    conn.SetTimeout(time.Duration(cfg.TimeoutSeconds) * time.Second)
    if cfg.StartTLS {
        if err := conn.StartTLS(&tls.Config{InsecureSkipVerify: cfg.SkipVerify}); err != nil {
            conn.Close()
            return nil, fmt.Errorf("starttls: %w", err)
        }
    }
    return conn, nil
}
```

认证三步（**顺序不能变**）：

```
① conn.Bind(cfg.BindDN, cfg.BindPassword)                 // 服务账号，仅用于搜索
② filter := buildUserFilter(cfg, username)
   search := ldap.NewSearchRequest(base_dn, ScopeWholeSubtree, DerefNever,
              []string{"dn", cfg.EmailAttr, cfg.NameAttr, cfg.UsernameAttr},
              buildFilter(cfg, username), nil)
   res := conn.Search(search)
   0 条 → 认证失败；>1 条 → 配置错误（日志 error）
③ 用 res.Entries[0].DN + 用户输入的密码，新建连接（或重新 Bind）→ 成功即认证通过
```

要点：

- **必须** `ldap.EscapeFilter(username)`，否则是 LDAP 注入。
- 过滤器的拼法与 Dex 保持一致：
  - `user_filter` 为空 → `(<username_attr>=<escaped>)`
  - 否则 → `(&<user_filter>(<username_attr>=<escaped>))`
- 第 ③ 步在**独立连接**上做，避免复用被服务账号 bind 过的连接。若复用同一连接的 `Bind`，注意 `conn.Bind` 会替换当前认证身份——可以复用，但必须确保第 ③ 步之后不再做需要服务账号权限的搜索。
- 取 `email`：为空直接返回错误（无法映射本地账号），提示检查 `email_attr`。
- 全程不打印密码；用户名用 `secutils.SanitizeForLog` 处理后再进日志。

**(d) 测试连接（给管理页用）**

在 `LDAPConfigLoader` 旁提供：

```go
type LDAPTestResult struct {
    Stage     string `json:"stage"`      // dial | bind | search | user_bind | ok
    LatencyMS int64  `json:"latency_ms"`
    Message   string `json:"message,omitempty"`
}

// TestConfig 用传入配置（bind_password 为空时取库里已存的），
// 依次执行 dial → bind → search(用 test_username，缺省则跳过) → user_bind(需要 test_password)
func (l *LDAPConfigLoader) TestConfig(ctx context.Context, cfg *types.LDAPAuthConfig,
    testUsername, testPassword string) *LDAPTestResult
```

分阶段返回是为了让运维一眼看出是网络、DN 写错、还是 baseDN/过滤器搜不到人。

### 5.4 P3：登录链路

**(a) `LoginWithLDAP`**

`internal/application/service/user.go`：

```go
func (s *userService) LoginWithLDAP(ctx context.Context, username, password string) (*types.LoginResponse, error)
```

流程（与 `LoginWithOIDC`（`:401`）结构对齐）：

1. `cfg, err := s.ldapLoader.Load(ctx)`；`ErrLDAPDisabled` → 返回 `Success:false`（handler 转 401）。
2. `ldapInfo, err := s.ldapLoader.Authenticate(ctx, cfg, username, password)` → 拿 `{DN, Email, Name, Username}`。
3. `user, err := s.userRepo.GetUserByEmail(ctx, email)`；not found → `provisionExternalUser(ctx, info, "ldap")`。
4. `if !user.IsActive` → `Success:false, Message:"Account is disabled"`。
5. `resolvedTenantID := s.resolveLoginTenantID(ctx, user)`；`generateTokensForTenant(ctx, user, resolvedTenantID)`。
6. 组装 `LoginResponse`（含 user / tenant / memberships），字段与本地 `Login` 完全一致，前端不用改类型。

**(b) 重构开户逻辑（小步、保行为）**

`provisionOIDCUser` / `generateOIDCUsername` 改为通用版本，OIDC 调用点保持行为不变：

```go
func (s *userService) provisionExternalUser(ctx context.Context, info *types.ExternalUserInfo, source string) (*types.User, error)
func (s *userService) generateExternalUsername(ctx context.Context, base, fallbackPrefix string) string
```

- `types.ExternalUserInfo{Username, Email, Name}`，OIDC 的 `types.OIDCUserInfo` 可直接映射。
- `Register` 之后：`if source == "ldap" && cfg.DefaultTenantID > 0 { memberService.AddMember(...viewer...); update user.TenantID }`。
- 开户成功后再写 `auth_source`（见 P4）。若 P4 未做，先留 TODO，但**不要**在建表前引用不存在的列。

**(c) `NewUserService` 注入**

`internal/application/service/user.go:68` 增加参数 `systemSettingSvc interfaces.SystemSettingService`，在内部构造 `&LDAPConfigLoader{settings: systemSettingSvc}`。

- 只改构造函数签名；`internal/container/container.go:198` 的 `Provide` 由 dig 按类型自动解析，不需要改。
- 当前仓库里没有其它 `NewUserService(` 调用点（已核对），不会破坏测试。

**(d) Handler**

`internal/handler/auth.go` 新增：

```go
type LDAPLoginRequest struct {
    Username string `json:"username" binding:"required"`
    Password string `json:"password" binding:"required"`
}

func (h *AuthHandler) LDAPLogin(c *gin.Context)
```

- 成功：200 + `types.LoginResponse`
- 认证失败：401 + `{"success":false,"message":"Invalid username or password"}`
- 账号禁用：403
- `ErrLDAPDisabled` / 配置错误：401 或 503（按 4.3 表）
- 日志里只出现 sanitized username

**(e) 探测接口**

扩展 `GetAuthConfig`（`internal/handler/auth.go:659`）返回值，**不新增端点**（前端已经在启动时调它，省一次请求）：

```json
{ "success": true, "registration_mode": "self_serve",
  "ldap_enabled": true, "ldap_display_name": "企业账号" }
```

`ldap_enabled` 必须读 DB（`GetJSONFresh` 或 `GetJSON`），不能用启动时缓存的配置——否则超管刚打开开关、登录页还是不显示。`ldap_display_name` 从配置里取（可先固定为 `"LDAP"`，字段留待后续扩展）。

**(f) 路由与白名单**

- `internal/router/router.go:702` 附近注册：`r.POST("/auth/ldap/login", publicAuthRL, handler.LDAPLogin)`
  - `publicAuthRL := middleware.PublicAuthRateLimit()` 已在 `:699` 定义，直接复用。
- `internal/middleware/auth.go:22` 的 `noAuthAPI` 增加：`"/api/v1/auth/ldap/login": {"POST"}`。
  **漏这一步 → 请求被 JWT 中间件拦截，表现为 401 而不是用户名密码错误**，是本项目最常见的新手坑。

### 5.5 P4：用户来源字段

**(a) 模型**

`internal/types/user.go:78` 增加：

```go
AuthSource string `json:"auth_source" gorm:"type:varchar(16);not null;default:'local';index"`
```

**(b) 迁移**

- `migrations/versioned/000065_users_auth_source.up.sql`
  ```sql
  ALTER TABLE users ADD COLUMN auth_source VARCHAR(16) NOT NULL DEFAULT 'local';
  CREATE INDEX IF NOT EXISTS idx_users_auth_source ON users(auth_source);
  ```
- 同名 `.down.sql` 删列删索引。
- **同时**更新 `migrations/sqlite/000000_init.up.sql`（users 建表处，`:221` 附近）加同名列，否则全新 SQLite 部署不会跑 versioned 迁移。

**(c) 写入与拦截**

- `Register` 增加 `AuthSource` 参数或后置 `UpdateUser`；OIDC/LDAP 开户后置为对应值，本地注册为 `local`。
- `Login`（`user.go:159`）：`if user.AuthSource != "" && user.AuthSource != "local"` → 返回 `Invalid email or password`（不要提示“请用 LDAP 登录”，避免泄露账号来源）。
- `ChangePassword`（`internal/handler/auth.go:604`）：同样拒绝，可提示“该账号由 LDAP/OIDC 管理”。

### 5.6 P5：前端

**(a) 管理页 `frontend/src/views/system/SystemSettings.vue`**

现有列表按 `value_type` 分支渲染（`:225/231/242`），需要：

- 新增 `v-else-if="item.value_type === 'json' && item.key === 'auth.ldap'"` 分支，渲染一个「编辑」按钮，点开专用抽屉/弹窗表单。
- 表单字段与 4.1 表一一对应；`bind_password` 用 `type="password"`，占位文案 `已配置，留空则不修改`；`bind_password_set` 为 true 时显示「已设置」标记。
- `server_url` 输入框提示 `ldap://host:389` 或 `ldaps://host:636`，并在前端做一次 scheme 校验（后端仍会再校验）。
- 「测试连接」按钮 → 调用新接口，把 `stage` 映射成人话（dial=网络不通，bind=服务账号 DN/密码错，search=baseDN 或过滤器错，user_bind=用户密码错）。
- 保存成功后若 `enabled=true`，提示“已生效，可立即用 LDAP 账号登录”。

**(b) API 层**

- `frontend/src/api/system/index.ts`：`SystemSettingItem.value_type` 联合类型加 `'json'`；新增 `testLDAPConfig(payload)`。
- `frontend/src/api/auth/index.ts`：新增 `ldapLogin({username,password})`，返回 `LoginResponse`（复用现有类型）。

**(c) 登录页 `frontend/src/views/auth/Login.vue`**

- `loadOIDCConfig`（`:584`）旁边加 `loadAuthConfig`，把 `ldap_enabled` 存到 `ldapEnabled`。
- 登录表单加页签/切换：`账号密码` | `LDAP`（受 `ldapEnabled` 控制）。LDAP 页签只有用户名 + 密码，提交走 `ldapLogin`。
- 复用现有 `handleLogin` 的成功分支（存 token、跳转），不要复制一套 token 处理逻辑。
- i18n 文案加到现有语言文件中（`auth.ldapLogin` 等），不要硬编码中文。

### 5.7 P6：收尾

- `.env.example`：若存在 `LDAP_*` 变量（线路 A 的 Dex 用），保留但加注释说明“线路 B 不使用”；新增/确认 `SYSTEM_AES_KEY` 说明（32 字节）。
- 本文档顶部「现状事实」表在实施后更新行号。
- 在 `docs/OIDC认证调用流程.md` 顶部加一句互见链接，说明两条路线的关系。

---

## 6. 安全清单（逐条勾选，缺一不可）

- [ ] `bind_password` 落库前加密；`GetAESKey()==nil` 时**拒绝保存**而不是静默存明文。
- [ ] `List` / `Get` / `Update` 返回值里没有 `bind_password`，只有 `bind_password_set`。
- [ ] 审计日志的 `old_value` / `new_value` 已脱敏（`emitChangeAudit` 原样落库，不改就是事故）。
- [ ] 解密失败时拒绝登录（fail-closed），不回退明文、不回退匿名 bind。
- [ ] 登录/测试连接路径用 `GetJSONFresh`，配置改动立即生效。
- [ ] 用户名过 `ldap.EscapeFilter`；日志里的用户名过 `secutils.SanitizeForLog`。
- [ ] 所有 LDAP 网络操作有超时（默认 5s），不允许登录请求无限挂起。
- [ ] 新登录接口在 `noAuthAPI` 白名单里，并挂 `PublicAuthRateLimit`。
- [ ] 认证失败统一文案，不区分“用户不存在”和“密码错误”。
- [ ] 测试连接接口只在 `SystemAdmin()` 路由组下。
- [ ] 密码不进任何 `logger.*` 调用，包括 `%v` 打印整个结构体。

---

## 7. 测试与验收

### 7.1 单元测试

| 文件 | 覆盖 |
|---|---|
| `internal/application/service/system_setting_test.go` | `json` 类型编解码、`Prepare` 留空保留旧密文、`Redact` 生效、`GetJSONFresh` 绕过缓存 |
| `internal/types/ldap_test.go` | `Normalize` 默认值、`Validate` 拒绝 `http://` 和缺端口、`Masked` 清空密码并置 `bind_password_set` |
| `internal/application/service/ldap_auth_test.go` | 过滤器拼装（空/非空 `user_filter`、特殊字符转义）、0/>1 条结果的错误分支；用假 connector 或对 `buildFilter` 纯函数测试，不要依赖真实 LDAP |
| `internal/handler/auth_ldap_test.go` | 401/403/503 映射；`GetAuthConfig` 返回 `ldap_enabled` |

### 7.2 手工验收（用 lldap 或真实 AD）

1. 不配置任何 LDAP → 登录页没有 LDAP 页签；直接 `POST /auth/ldap/login` 返回 401。
2. 超管在设置页填入错误 `server_url`（如 `http://host:3890`）→ 保存被 400 拒绝。
3. 填 `ldap://host:3890` + 正确 bindDN/密码 + baseDN → 测试连接返回 `ok`。
4. 打开开关 → **不重启服务**，刷新登录页出现 LDAP 页签。
5. 用 LDAP 用户首次登录 → 自动开本地账号、建个人空间、拿到 token；`/auth/me` 正常。
6. 用错误密码 → 401，统一文案。
7. 在设置页把 `bind_password` 留空直接保存其他字段 → 旧密码仍可用（`Prepare` 生效）。
8. 查 `system_settings` 表：`value->>'bind_password'` 以 `enc:v1:` 开头。
9. 查审计表：`old_value`/`new_value` 中无密码。
10. 关掉开关 → 已登录用户的 JWT 仍有效（这是预期，token 不因配置变更失效），但新登录被拒。
11. 该 LDAP 用户尝试用邮箱 + 随机密码走 `/auth/login` → 被拒。
12. 多副本（有 Redis）：A 副本改配置，B 副本登录立即生效。

---

## 8. 已知坑与报错对照

| 现象 | 原因 | 处理 |
|---|---|---|
| `too many colons in address` / `dial tcp http://...:389` | `server_url` 写了 `http://`，Dex/库把它当 host 再拼 `:389` | 只用 `ldap://host:port` 或 `ldaps://host:port` |
| `LDAP Result Code 64 Naming Violation` | bindDN 少了 `ou=people`（lldap 固定 `uid=...,ou=people,dc=...`） | 补全 DN |
| `tls: first record does not look like a TLS handshake` | 对明文端口（lldap 3890）默认按 ldaps 连 | `ldap://` + `start_tls=false` + `skip_verify=false` |
| 搜索 0 条但用户确实存在 | `username_attr` 写成 AD 的 `sAMAccountName`，或 `base_dn` 少了 `ou=people` | lldap 用 `uid`；先手工 `ldapsearch` 验证 |
| `filter returned multiple (N) results` | `user_filter` 为空且目录里有同名条目 | 补 `(objectClass=person)` 之类条件 |
| 登录成功但没建个人空间 | `Register` 失败被吞（邮箱/用户名冲突） | 检查 `provisionExternalUser` 的错误是否上抛 |
| 前端不显示 LDAP 页签 | `GetAuthConfig` 读了启动时缓存，或未加 `noAuthAPI` | 用 `GetJSONFresh`；检查白名单 |
| 保存成功但登录仍用旧配置 | 登录路径走了 cache | 改用 `GetJSONFresh` |
| 解密报 `ErrEncryptedDataMissingKey` | `SYSTEM_AES_KEY` 变更/未设置 | 重新录入密码；文档说明轮换流程 |

---

## 9. 交付物清单

**新增**

- `internal/types/ldap.go`
- `internal/application/service/ldap_auth.go`
- `migrations/versioned/000065_users_auth_source.{up,down}.sql`
- `internal/application/service/ldap_auth_test.go`、`internal/types/ldap_test.go`
- 前端：LDAP 配置表单组件（放在 `frontend/src/views/system/` 下）

**修改**

- `internal/application/service/system_setting.go`（json 类型、registry、Prepare/Redact、GetJSON*、Update 顺序）
- `internal/types/interfaces/system_setting.go`、`internal/types/interfaces/user.go`
- `internal/types/system_setting.go`（如需 `AsJSON`）
- `internal/types/user.go`（`AuthSource`）
- `internal/application/service/user.go`（`LoginWithLDAP`、`provisionExternalUser`、`NewUserService`）
- `internal/handler/auth.go`（`LDAPLogin`、`GetAuthConfig`、`ChangePassword`）
- `internal/handler/system.go` + `internal/router/router.go`（测试连接接口、路由）
- `internal/middleware/auth.go`（`noAuthAPI`）
- `migrations/sqlite/000000_init.up.sql`
- `frontend/src/api/system/index.ts`、`frontend/src/api/auth/index.ts`、`frontend/src/views/system/SystemSettings.vue`、`frontend/src/views/auth/Login.vue`、i18n 文案
- `.env.example`

**验收口径**：7.2 全部通过 + `go build ./...` + `go test ./internal/application/service/... ./internal/types/...` 绿。
