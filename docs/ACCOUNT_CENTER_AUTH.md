# CatsCo 账号中心接入文档

本文面向公司内部业务服务。目标是让多个产品共用同一套 CatsCo 用户账号：用户只需要用 CatsCompany 账号登录，业务服务通过账号中心确认“这个用户是谁、账号是否可用”，再决定自己的业务权限。

当前账号中心暂时内置在 CatsCompany 服务里。代码边界已经尽量保持独立，后续可以拆成独立认证服务。

## 一句话理解

- 用户登录凭证：用户自己的 JWT，来自 CatsCompany 登录接口。
- Service Token：内部业务服务访问账号中心的服务端凭证，不属于某个用户。
- 业务服务不要直连 CatsCompany 数据库，也不要在前端保存 Service Token。

## 账号状态

用户资料里的 `state` 用于限制账号：

- `0`：正常，可登录、可通过账号中心校验。
- `1`：已禁用，不能登录；已有 JWT 访问 CatsCompany API 会被拒绝；机器人 API Key 和 WebSocket 连接会被拒绝；账号中心 introspect 会返回不可用，业务服务应拒绝访问。

本地账号后台可以禁用或恢复用户账号。这个能力用于风控、测试账号隔离、异常账号处理。

## 接入链路

```text
用户在 CatsCompany 登录
  -> 得到用户 JWT
  -> 用户访问某个业务服务
  -> 业务服务后端携带 Service Token 调账号中心
  -> 账号中心验证用户 JWT 并返回用户资料
  -> 业务服务按 uid / account_type / state / 自己的业务角色放行或拒绝
```

## 第一步：创建 Service Token

Service Token 是业务服务调用账号中心的内部凭证。它只放在服务端环境变量、Secret Manager 或 CI/CD Secret 中，不能放到浏览器、桌面客户端或公开仓库。

后台入口仅供 SSH 隧道访问，不能暴露到公网。生产 nginx 配置应显式拦截 `/local` 和 `/local/` 路径；后端也会拒绝带公网 `X-Forwarded-For` / `X-Real-IP` / `Forwarded` 的代理请求。因为 Docker 端口转发可能让 SSH 隧道在容器内表现为私网 bridge 地址，所以后台页面仍允许本机和私网 bridge 来源；不要把后端端口绑定到公网或内网负载均衡。

```bash
ssh -N -L 26061:127.0.0.1:26061 <server-alias>
```

然后在本机浏览器打开：

```text
http://127.0.0.1:26061/local/account-admin
```

在 `Service Token` 区域创建一个服务：

- 服务标识：稳定的机器名，例如 `writing-app`、`ops-tool`、`internal-api`。
- 显示名称：给人看的名称，例如 `写作平台`、`运维工具`。
- 权限范围：可选。留空表示允许使用当前全部账号中心接口；一旦选择了权限，账号中心会按接口强制检查。

常用权限：

- `account.introspect`：验证用户登录状态。
- `account.users.read`：按 UID 读取用户基础资料。

创建后会显示一次性明文 token。请立刻保存到对应业务服务的 secret 或环境变量里。数据库只保存 hash 和 token 前缀，后续无法找回明文；丢失或疑似泄露时，在后台重新生成并替换业务服务配置。

兼容旧部署时，也可以通过环境变量预置 service token，格式为 `slug=plain-token` 或 `slug=sha256:<hex-encoded-sha256>`，多个条目可用逗号、分号或换行分隔。优先推荐用后台创建数据库 token，便于撤销和轮换。

## 第二步：用户登录

用户仍然走 CatsCompany 登录接口：

```http
POST /api/auth/login
Content-Type: application/json

{
  "account": "user@example.com",
  "password": "******"
}
```

成功返回：

```json
{
  "token": "<user_jwt>",
  "uid": 2,
  "username": "demo-user",
  "email": "demo@example.com",
  "display_name": "Demo User",
  "avatar_url": "/uploads/avatar.png",
  "account_type": "human"
}
```

业务前端或客户端访问自己的业务后端时，携带用户 JWT：

```http
Authorization: Bearer <user_jwt>
```

## 第三步：业务后端验证用户

业务后端收到用户 JWT 后，调用账号中心：

```http
POST /api/account/introspect
Authorization: Service <service_token>
Content-Type: application/json

{
  "token": "<user_jwt>"
}
```

有效用户返回：

```json
{
  "active": true,
  "user": {
    "uid": 2,
    "username": "demo-user",
    "email": "demo@example.com",
    "display_name": "Demo User",
    "avatar_url": "/uploads/avatar.png",
    "account_type": "human",
    "state": 0
  },
  "claims": {
    "issuer": "catscompany",
    "issued_at": "2026-05-21T12:00:00Z",
    "expires_at": "2026-05-28T12:00:00Z"
  }
}
```

用户 JWT 无效或过期时返回：

```json
{
  "active": false,
  "error": "invalid_or_expired_token"
}
```

账号不存在或已禁用时返回：

```json
{
  "active": false,
  "error": "user_not_available"
}
```

业务服务建议：

- `active=true` 且 `user.state=0` 才放行。
- `active=false` 时，业务服务应该向自己的调用方返回 401 或 403。用户 JWT 无效或过期时引导重新登录；账号被禁用时展示“账号不可用”。
- 账号中心验证结果可以短缓存 30-120 秒，减少重复请求。
- 自己业务里的角色、套餐、配额、权限仍然由业务服务自己管理。

## 第四步：按 UID 查询用户资料

业务服务需要补全用户资料时，可以调用：

```http
GET /api/account/users/{uid}
Authorization: Service <service_token>
```

返回：

```json
{
  "uid": 2,
  "username": "demo-user",
  "email": "demo@example.com",
  "display_name": "Demo User",
  "avatar_url": "/uploads/avatar.png",
  "account_type": "human",
  "state": 0,
  "created_at": "2026-05-01T08:00:00Z"
}
```

普通前端不要直接调用这个接口。普通用户查看自己的资料仍然使用：

```http
GET /api/me
Authorization: Bearer <user_jwt>
```

注意：`GET /api/account/users/{uid}` 是资料补全接口，不等于鉴权结论。它会返回用户当前状态字段；业务鉴权仍应以 `/api/account/introspect` 为准。

## 接入方伪代码

### Node / TypeScript

```ts
async function verifyCatsUser(userToken: string) {
  const resp = await fetch(`${process.env.CATS_ACCOUNT_CENTER_URL}/api/account/introspect`, {
    method: 'POST',
    headers: {
      Authorization: `Service ${process.env.CATS_SERVICE_TOKEN}`,
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ token: userToken }),
  });

  if (!resp.ok) {
    throw new Error(`account center failed: ${resp.status}`);
  }

  const result = await resp.json();
  if (!result.active || result.user?.state !== 0) {
    return null;
  }

  return result.user;
}
```

### Go

```go
func verifyCatsUser(ctx context.Context, accountURL, serviceToken, userToken string) (*CatsUser, error) {
	body, _ := json.Marshal(map[string]string{"token": userToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, accountURL+"/api/account/introspect", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Service "+serviceToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("account center status %d", resp.StatusCode)
	}

	var out struct {
		Active bool      `json:"active"`
		User   *CatsUser `json:"user"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.Active || out.User == nil || out.User.State != 0 {
		return nil, nil
	}
	return out.User, nil
}
```

## 错误处理

### Service Token 验证器不可用

```http
HTTP/1.1 503 Service Unavailable
```

```json
{
  "error": "account center service tokens are not configured"
}
```

说明账号中心的 service token 验证器或数据库存储不可用。正常部署里，如果只是还没有创建 token、token 填错或 token 已撤销，通常会返回下面的 401。

### Service Token 无效

```http
HTTP/1.1 401 Unauthorized
```

```json
{
  "error": "invalid service token"
}
```

说明业务服务端传错了 `Authorization: Service <service_token>`。

### Service Token 权限不足

```http
HTTP/1.1 403 Forbidden
```

```json
{
  "error": "service scope denied"
}
```

说明该 service token 已配置权限范围，但缺少当前接口需要的 scope：

- `POST /api/account/introspect` 需要 `account.introspect`。
- `GET /api/account/users/{uid}` 需要 `account.users.read`。

如果 token 没有配置任何 scope，则兼容为允许使用当前全部账号中心接口。

### 用户 JWT 无效

```http
HTTP/1.1 200 OK
```

```json
{
  "active": false,
  "error": "invalid_or_expired_token"
}
```

说明用户 token 格式不对、签名无效或已经过期。业务服务通常让用户重新登录。

### 用户不存在或账号已禁用

```http
HTTP/1.1 200 OK
```

```json
{
  "active": false,
  "error": "user_not_available"
}
```

说明用户账号不存在或已被后台禁用。业务服务应该拒绝访问，并按产品需要提示“账号不可用”。

## 安全要求

- Service Token 只能放在业务服务端。
- Service Token 不进前端、不进桌面客户端、不进 Git。
- 业务服务不要直连 CatsCompany 数据库。
- 业务服务不要保存用户密码。
- 业务服务必须通过 `/api/account/introspect` 验证用户身份。
- 生产环境必须配置固定 `OC_JWT_SECRET`。
- 业务服务自己维护业务角色、业务授权和业务数据。

## 新服务接入清单

1. 确定服务标识，例如 `writing-app`。
2. 通过本地后台创建 service token。
3. 把 service token 放入该服务的 secret 或环境变量。
4. 前端或客户端让用户使用 CatsCompany 登录。
5. 客户端请求业务后端时带 `Authorization: Bearer <user_jwt>`。
6. 业务后端调用 `/api/account/introspect`。
7. 业务后端按 `uid`、`account_type`、`state` 和自己的业务权限放行或拒绝。
8. 需要展示用户资料时，业务后端调用 `/api/account/users/{uid}`。

后续如果账号中心拆成独立服务，优先保持这两个接口兼容：

- `POST /api/account/introspect`
- `GET /api/account/users/{uid}`
