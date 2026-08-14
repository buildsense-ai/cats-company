# Dreamina Worker 基线

更新时间：2026-08-13

这份文档记录当前 Dreamina worker 已经承诺的行为。它是后续调优和排查问题时的参照，不代表 Dreamina 上游永远可用，也不替代真实环境验收。

## 1. 当前定位

Dreamina worker 是一个独立的异步图片生成服务，负责把统一的图片生成请求转换成 Dreamina CLI 任务，并保存任务状态、提交命令、查询结果和最终图片。

它不是图片生成 skill 本身，也不负责理解用户自然语言。自然语言到结构化请求的转换仍由上游 agent/skill 完成。

当前主要入口：

- `POST /v1/images/generations`：文生图
- `POST /v1/images/edits`：参考图生图
- `GET /v1/tasks/:task_id`：查询异步任务
- `GET /healthz`：健康检查

## 2. 请求边界

上游应尽量发送结构化字段：

```json
{
  "prompt": "A clean architectural visualization",
  "size": "3840x2160",
  "output_format": "png"
}
```

参考图生图在 `images` 中携带 1-3 张 data URL 图片：

```json
{
  "prompt": "Keep the character identity and change the background",
  "size": "3840x2160",
  "images": [
    { "image_url": "data:image/png;base64,..." }
  ]
}
```

当前统一尺寸字段是 `size`，例如 `1024x1024`、`1536x1024`、`3840x2160` 或 `auto`。不要依赖 prompt 中的“4K”文字让 worker 自己猜分辨率。

## 3. 分辨率映射

worker 会把统一请求中的 `size` 映射成 Dreamina CLI 的分辨率档位：

| 请求情况 | Dreamina 参数 |
| --- | --- |
| `size` 缺省或为 `auto` | `--resolution_type=2k` |
| 2K 档位尺寸，例如 `2560x1440` | `--resolution_type=2k` |
| 明确的 4K 尺寸，最长边为 3840，例如 `3840x2160` | `--resolution_type=4k` |

这个映射同时用于 `text2image` 和 `image2image`。

典型的 4K 请求：

```text
3840x2160 -> --ratio=16:9 --resolution_type=4k
2160x3840 -> --ratio=9:16 --resolution_type=4k
3840x3840 -> --ratio=1:1 --resolution_type=4k
```

这里的 `4k` 是提交给 Dreamina 的生成档位，不是 worker 对最终文件尺寸的强行放大。当前统一请求允许的最大边长是 3840，因此只有明确要求 3840 边长时才切换到 4K；普通 2K 请求即使是 `2560x1440` 也保持 2K 档位。最终尺寸仍必须以返回文件的真实像素为准。

## 4. 任务流程

1. 服务接收请求并校验 prompt、参考图和基本尺寸格式。
2. 服务为任务创建独立目录，保存 `request.json` 和参考图。
3. runtime 读取统一请求，必要时压缩/准备参考图。
4. runtime 根据是否有参考图选择 `text2image` 或 `image2image`。
5. runtime 根据 `size` 选择 `2k` 或 `4k`，生成并保存 `dreamina-command.json`。
6. worker 提交 Dreamina CLI，记录 submit id。
7. worker 轮询 `query_result`，直到成功、失败或进入 pending。
8. 成功后下载图片，做本地图片格式和尺寸读取，并写入 `result.json`。
9. HTTP 层把结果转换为兼容图片生成接口的响应。

任务目录是排查问题的关键证据，通常应重点看：

- `request.json`：worker 实际收到的结构化请求
- `dreamina-command.json`：实际提交给 CLI 的命令和参数
- `worker-task.json`：提交、轮询和恢复状态
- `result.json`：最终输出、实际尺寸和 warnings
- `provider/`：credit、submit、query 的原始摘要

## 5. 当前保证与非保证

### 已保证

- 文生图和参考图生图使用不同的 Dreamina CLI 操作。
- 参考图会落盘到当前任务目录，不依赖下一轮对话重新传输。
- 任务提交前会保存命令和状态，已有 submit id 时恢复查询不会重复提交。
- 默认请求不会因为 prompt 中出现“高清”就自动切到 4K。
- 明确的高尺寸请求会提交 Dreamina `4k` 档位。
- 输出文件会进行基本图片格式校验，并记录真实宽高。

### 尚不保证

- Dreamina 上游、登录状态、额度、中转服务和网络始终可用。
- 所有 Dreamina 模型版本都支持所有分辨率档位。
- provider 返回的像素一定与请求的 `size` 完全一致。
- 生成结果的主体、人物身份、构图或审美质量自动正确。
- worker 本身具备多模态语义审核能力。

如果请求尺寸和实际输出尺寸不一致，不能只看 prompt 或 CLI 参数，要以 `result.json` 和图片真实像素为准。

## 6. Image2 的关系

本次修复只发生在 Dreamina provider 适配器：

```text
LLM/skill -> size -> Dreamina worker -> --resolution_type=2k/4k
```

Image2 仍使用原有的统一请求字段和自己的 provider 转换逻辑，不读取 Dreamina 的 `resolution_type`，因此不会因为这次修复改变行为。

## 7. 验收基线

本地自动测试：

```powershell
cd services/dreamina-worker
npm test
```

至少应覆盖：

- 普通文生图包含 `--resolution_type=2k`
- `3840x2160` 文生图包含 `--resolution_type=4k`
- `3840x2160` 参考图生图包含 `--resolution_type=4k`
- 参考图生图使用 `image2image`
- 任务查询不会因为恢复而重复提交

真实环境验收时，应检查任务目录中的 `dreamina-command.json`：

```text
--resolution_type=4k
```

并检查最终图片的真实尺寸是否为目标规格，例如：

```text
请求：3840x2160
实际：3840x2160
```

如果命令是 `4k` 但实际仍不是目标尺寸，问题已经从 worker 的“档位选择”转移到 Dreamina CLI、模型版本或上游返回结果，不能继续靠改 prompt 解决。

## 8. 暂不纳入本次改动

- 不新增公共 `resolution_tier` 字段。
- 不修改 Image2 provider。
- 不扫描 prompt 关键词推断 4K。
- 不增加自动放大、超分或二次图片处理。
- 不改变重试、fallback、登录和部署策略。
