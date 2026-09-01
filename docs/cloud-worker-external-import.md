# 手动云员工登记

手动部署、带公网地址的天翼云实例统一登记到 `cloud_worker_bindings`，默认固定为：

```text
management_mode = manual_import
lifecycle_mode  = external
```

这类记录只进入内部 CommercialOps 云员工总览，不会创建
`cloud_worker_lifecycles`，因此不会被平台自动续费、退订、重置、销毁或版本更新任务处理。

## 内部接口

接口只通过 relay-admin 的本机/内网路径访问，并要求 `commercial.ops.write`：

```text
POST /local/commercial-ops/api/cloud-workers/import
```

请求至少需要 `owner_uid`、`provider`、`region_id`、`instance_id` 和
`instance_name`。同一 `provider + instance_id` 重复提交是幂等更新。

导入前仍应由管理员从天翼云 CLI 重新核对实例 ID 和状态；导入接口本身不会调用
云厂商写操作。
