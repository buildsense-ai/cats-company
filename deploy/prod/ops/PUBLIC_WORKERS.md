# 云员工双部署方案

- `private_nat`：普通付费创建权益的默认方案，使用现有华南 2 NAT 配置。
- `public_ip`：后台手动补发或邀请码授予，使用佛山 7 独立网络和公网 IP。前台没有方案选择字段，创建时从有效权益中选择；有公网权益时优先使用。后台自动部署可明确指定方案，不会消耗另一方案的权益。
- 每台机器生成独立 SSH 密钥。Relay 后台「云员工 → 详情 / SSH 连接」显示公网/内网地址、用户名、端口、密钥在 `cats-ctyun` 的位置及连接命令。命令在 `cats-ctyun` 执行；公网机直连，NAT 机经过 jump。外部登记机器不假定平台持有密钥。

## 配置和上线顺序

1. 合并 XiaoBa 的双资源池制镜支持，在受保护的 `main` 工作流选择 `public_ip` 制作并验收佛山 7 Worker 镜像。不要复用历史业务实例镜像。
2. CatsCompany `prod` 环境变量 `CATSCO_WORKER_PUBLIC_PROFILE_JSON` 配置下面的非敏感 JSON。留空时停用新的公网创建，已创建机器继续使用自身快照。
3. 合并 CatsCompany，正常运行 Deploy Docker Test → Deploy Docker Prod。CD 同步配置并更新 Artifact 网关的路由工具，不重建网关网络、证书或现有路由。
4. 部署关联 Relay 后台 PR，验证两类方案、邀请兑换、SSH、更新/重置和 Artifact 的实际链路。

```json
{
  "CTYUN_WORKER_REGION_ID": "200000004421",
  "CTYUN_WORKER_PROJECT_ID": "<worker-enterprise-project>",
  "CTYUN_IMAGE_PROJECT_ID": "<image-enterprise-project>",
  "CTYUN_WORKER_AZ_NAME": "cn-gd-fos7-1a-public-citycloud",
  "CTYUN_WORKER_FLAVOR_ID": "<flavor-id>",
  "CTYUN_WORKER_VPC_ID": "<dedicated-vpc-id>",
  "CTYUN_WORKER_SUBNET_ID": "<dedicated-subnet-id>",
  "CTYUN_WORKER_SECURITY_GROUP_ID": "<dedicated-security-group-id>",
  "CATSCO_WORKER_HTTP_BASE_URL": "https://app.catsco.cn",
  "CATSCO_WORKER_SERVER_URL": "wss://app.catsco.cn/v0/channels"
}
```

HTTP/WS 默认使用 `.cn`；需要 `.cc` 时成对设置对应地址，两边前台均通过同一授权创建链路使用机器。公网配置不能省略资源 ID 后借用 NAT 配置。公网方案强制 `extIP=1`，关闭机器 SSH 的 NAT 跳板；Artifact 网关的 SSH 配置单独保留。安全组使用 TCP 22 和 19900–20000，保留正常出站；不依赖 80/443/8080 入站。

`bot_config.cloud_deployment` 保存实例的非敏感配置，创建前落库；供给脚本再保存租户目录的 `deployment.json`。更新、重置、续费、销毁根据该快照定位实例。不要往 JSON 中加入 AK/SK、令牌或密钥内容，也不要手工修改现有实例的区域字段。

状态按配置分别查询，单一区域失败显示 `unavailable`，不判为删除。Artifact 同步逐租户读取配置，任何区域查询失败都中止完整替换，保留上次路由；网关继续使用既有 `private_ip` 字段存储后端地址，额外的 `network_mode` 区分公网和私网。

## 验收与回退

自动测试覆盖权限分流、真实 PostgreSQL 迁移和邀请码、区域失败隔离、脚本公网选址与路由同步。自动测试不代表公网实例已创建并验收。

正式验收需要临时账号从后台发放或兑换开始，确认公网 IP、独立密钥登录、员工连接、实际工具执行、Artifact 发布/访问、更新/重置，以及 NAT 付费路径和 `.cn`/`.cc`。记录实例、账号和测试数据标识并清理。

发现问题先将公网配置置空并经原 CD 停止新增，保留 NAT 服务及现有机器。产生公网实例后，不得直接降级到不认识快照的旧控制面，否则旧脚本可能查询错误区域；先恢复支持快照的版本或完成受控资源处置。网关一旦保存公网路由，也须保留兼容该格式的路由工具。
