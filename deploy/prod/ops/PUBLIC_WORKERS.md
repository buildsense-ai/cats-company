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

## 按量试用与包月

网络方案和计费方式独立。普通付费创建默认包月；后台内部套餐、邀请码、权益发放和部署可选择 `ondemand` 试用，试用必须有截止时间，按量套餐只能为隐藏套餐。前台不能通过请求字段更改计费方式。生命周期和部署快照共同记录创建时的选择。

试用到期在用户云员工面板提示付款，自动调用节省关机，并确认云端 `shelve` 状态；失败不降级成普通关机，也不进入自动释放。确认关机后至少保留 3 天，未转换则释放。磁盘和公网 IP 等残余费用仍可能产生，由平台承担。后台支持筛选计费方式、查看截止/释放时间，操作节省关机、恢复、转包月和提前释放。

付款续费和后台转包月均转换原实例。转换意图先落库以阻断旧释放计划；确认云端 `onDemand=false`、未来到期时间、关闭自动续费且恢复运行后，才更新平台包月状态。转换接口没有幂等 token，租户目录的 `conversion-requested` 标记防止结果未知时重复购买。未知结果必须核对云端订单，不能盲目删除标记；取消转换仅允许云端仍为按量且没有请求标记。操作锁超过 30 分钟可重新核对，释放仍受生命周期条件保护。

2026-09-09 佛山 7 的隔离公共 Ubuntu 实例实测：创建、SSH、节省关机、恢复、原实例转包月、禁用自动续费、退订和销毁均获得状态确认。按流量 EIP 转包月失败 `Ecs.EipCheck.NotValid`，按带宽 EIP 转换成功，因此公网试用创建采用按带宽 EIP。测试产生过真实月度订单，尚未核对净账单金额。测试实例、云密钥、EIP 和磁盘已清理，专用网络保留。

这些 API 实测不替代正式 Worker 镜像和应用验收。华南 2 按量节省关机/转包月尚未实测，验收前不要发放该区域试用。公网配置仍须等待新镜像与 CN/CC 实际应用链路验收。产生试用实例后，回退也必须保留按量生命周期和转换意图处理能力，不能退回仅懂包月的旧控制面。
