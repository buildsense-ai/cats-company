# 托管 Slurm CPU 试点 worker

在 Slurm 登录节点用 Python 3.9+ 运行。`srun` 和 `/usr/bin/awk` 需要在计算节点可用。Cats token 和天翼云凭据分别由环境变量 `CATS_COMPUTE_WORKER_TOKEN`、`CTYUN_AK`、`CTYUN_SK` 注入。配置文件保存端点、资源和 API 字段映射。

```sh
python3 -B cmd/hpc-task-worker/worker.py --config /path/to/config.json --preflight
python3 -B cmd/hpc-task-worker/worker.py --config /path/to/config.json
python3 -B -m unittest discover -s cmd/hpc-task-worker -v
```

启动检查读取指定的既有云作业状态，并验证 Slurm CLI、调度器和会计数据查询。完整运行要求 Cats 的 `save` 续租 45 秒，`finish` 根据完整 progress 验证和结算。worker 在外部查询前续租，在任何非幂等操作前确认服务端已持久化 intent。请在同一配置下恢复同一批任务并保持 worker 和 Cats 的 UTC 时钟同步。

试点队列需要限制为固定 worker 账号提交，计算节点也需要专用于该分区。worker 身份必须能通过 `squeue --all` 看到涉及这些节点的所有作业。回收前会检查节点和分区两个范围。Slurm 的读取与云缩容之间存在时间窗口，因此需要由集群配置保障独占访问。

## 配置

顶层 `cats_url` 是 Cats 的 HTTPS base URL。`slurm.account` 是固定 Slurm 账户，`slurm.srun` 可配置计算节点上的绝对路径，默认 `/usr/bin/srun`。

`cloud` 下配置以下字段。

| 字段 | 值 |
| --- | --- |
| `hosts.hpc`、`hosts.ecs`、`hosts.evs` | 对应服务的官方 HTTPS base URL |
| `regionID`、`azName`、`clusterUUID` | 固定的省区、可用区、集群 |
| `compute_role_uuid` | 计算角色的 `clusterTypeRoleUUID` |
| `subnet`、`system_disk_type`、`system_disk_size_gb` | 扩容用的普通子网、系统盘类型与容量 |
| `skus` | 以 Cats SKU 为键，每项提供 `imageUUID`、`specificationUUID` 和节点列表中的 `specificationName` |
| `default_queue_value`、`nondefault_queue_value` | 默认、非默认队列分别填 `true`、`false` |
| `probe_job_uuid`、`pending_job_statuses` | 可读取的既有云作业 UUID，非终态为 `wait`、`pending_submit`、`submitted` |
| `api` | 下表中各操作的绑定 |

`api` 中每项配置 `fields`。键是下表中的语义名，值是响应 JSON 的点分路径，如 `returnObj.jobUUID`。集合操作的 `fields` 路径相对于单个列表元素。除内置路径外，每项还配置 `path`、`method` 和 `params`。`params` 的键为下表的语义名，值为 API 实际请求参数名。POST JSON 可使用点分路径表达嵌套对象。支持 `regionID`、`azName` 和 `clusterUUID` 作为额外的作用域参数，也可给 `shrink` 加 `queueUUID`。

| 操作 | 请求参数语义名 | 响应字段语义名 |
| --- | --- | --- |
| `create_queue` | 内置 POST `/v4/cthpc/hpc/create-queue` | `jobUUID` |
| `job` | 内置 GET `/v4/cthpc/query-job-result`，以 `jobUUID` 查询 | `status`，可选 `jobUUID` |
| `list_queues` | GET `/v4/cthpc/hpc/list-queues`，`regionID`, `azName`, `clusterUUID` | `queueName`, `queueUUID`, `default` 映射到 `isDefault` |
| `list_nodes` | GET `/v4/cthpc/hpc/list-nodes`，`regionID`, `azName`, `clusterUUID` | `queueName`, `nodeUUID`, `nodeName`, `specificationName` 同名，`instanceID`, `role`, `kind`, `billing`, `creation` 分别映射 `resourceInstanceID`, `nodeRole`, `nodeType`, `onDemand`, `scalingType` |
| `expand` | 内置 POST `/v4/cthpc/hpc/expand-cluster`，按量新建 `ecsComputeNodeOrders` | `jobUUID`，可选 `masterOrderID` |
| `shrink` | POST `/v4/cthpc/hpc/shrink-cluster`，`regionID`, `azName`, `clusterUUID`, `nodeUUIDList` | `jobUUID` |
| `delete_queue` | POST `/v4/cthpc/hpc/delete-queue`，`regionID`, `azName`, `clusterUUID`, `queueUUID` | `jobUUID` |
| `ecs` | `regionID`, `instanceID` | `id`, `volumeID`，其中 `volumeID` 映射到 `returnObj.attachedVolume` |
| `evs` | GET `/v4/ebs/info-ebs`，`regionID` 同名，`volumeID` 映射到 `diskID` | `id`, `instanceID`, `system` 分别映射 `returnObj.diskID`, `returnObj.instanceID`, `returnObj.isSystemVolume` |

`list_queues` 和 `list_nodes` 的 `items` 都是 `returnObj.resultList`。队列列表使用 `unpaged: true`。节点列表使用 `total: "returnObj.totalCount"`、`first_page: 1`，将 `page`、`page_size` 分别映射到 `pageNo`、`pageSize`。worker 要求分页总数稳定，节点须属于任务队列并匹配计算角色 `1`、`VM`、按量及 `manual`。队列变更与缩扩容的操作回执字段使用 `returnObj.jobUUID`，操作状态使用 `returnObj.status`。

ECS 和 EVS 支持按精确 ID 过滤的集合或单条查询。单条查询需配置 `error_code` 路径和 `absent_codes` 数组。ECS 已释放响应的 `error` 为 `Ecs.Instance.NotFound`，EVS 已释放响应的 `errorCode` 为 `ebs.ebsInfo.notExists`。可为创建队列和扩容配置 `error_code` 与 `rejected_codes`，仅映射供应商明确保证未创建资源的拒绝码。接口需要 HTTP 200 和 EOP `statusCode=800` 才视为成功。`test_worker.py` 的绑定对应隔离测试。

## 恢复

`reconcile` 的 `problem` 以前一次 intent 或检查阶段开头。worker 在后续领取时从该阶段恢复。若缺失云端操作 `jobUUID`，可用 intent 中提前记录的 EOP request ID 查供应商记录。确认原操作属于此任务后，将其 `jobUUID` 补入相应 `*_operation` 字段。`force_completed` 或失败云作业进入对账。未知 `sbatch` 结果由完整 quote ID 派生的 Slurm job name 通过 `squeue` 和 `sacct` 恢复。若两个接口均未找到匹配项，worker 继续保留对账状态。

`released` 提交已完成的云回收和 Slurm 会计记录。`rejected` 提交明确拒绝且无容量开通的结果。金额由 Cats 服务端计算。

## 多实例运行

多个 worker 可以使用同一份 Cats 地址、配置文件和凭据并行领取任务。数据库会在 claim 事务内统一执行节点数、任务数和用户配额限制，worker 数量按容量上限部署即可。

仓库提供 `deploy/prod/ops/cats-hpc-task-worker@.service` 模板。每个实例读取 `/etc/catsco/hpc-worker-%i.json`，共享 `/etc/catsco/hpc-task-worker.env` 中的 `CATS_COMPUTE_WORKER_TOKEN`、`CTYUN_AK` 和 `CTYUN_SK`。服务使用 `galaxy` 用户运行，配置中的 `slurm.account` 固定为 `galaxy`，`slurm.srun` 必须填写计算节点上的绝对路径。

部署前先对每个配置执行一次 `--preflight`，再启用实例。例如 `cats-hpc-task-worker@1.service` 和 `cats-hpc-task-worker@2.service` 可以使用相同配置并行消费，实例数量由 `CATSCOMPANY_COMPUTE_MAX_CONCURRENT_TASKS` 和天翼云 API 配额共同决定。

systemd 模板让同一台主机上的实例共享 `/var/lib/catsco-hpc-worker/mutation`，并通过 `CATS_HPC_MUTATION_LOCK_FILE` 指定该路径。手动启动时可以用 `cloud.mutation_lock_file` 指定共享的绝对路径，默认使用系统临时目录下的 `catsco-hpc-task-worker.mutation`。worker 会在状态文件旁创建 `.lock` 文件，目录须可读写。`cloud.mutation_lock_timeout_seconds` 默认 5 秒，`cloud.mutation_cooldown_seconds` 默认 0.2 秒。worker 获得锁后先保存意图并续租，再发送 create、expand、shrink 或 delete 请求。等锁超时会继续等待。请求已发出但结果未知时走现有恢复流程。
