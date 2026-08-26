# CatsCo 官网工程交付说明

本目录来自同事交付的官网预览，并已接入 CatsCompany 的独立官网分支。

打包时间：2026-08-25 15:43（Asia/Shanghai）

## 本地运行

```bash
pnpm install --frozen-lockfile
pnpm run dev
```

## 上线前检查

```bash
pnpm test
pnpm run typecheck
pnpm run build
```

当前检查结果：测试 9 项通过，类型检查通过，生产构建通过。

## 说明

- 已包含当前预览所需的源码、样式、设计系统、公共媒体、测试和部署配置。
- 未包含 `.git`、`node_modules`、`dist` 和本机 TypeScript 缓存文件。
- 登录、注册、套餐选择和桌面端下载入口会安全跳转到 `app.catsco.cc`，由现有工作台完成认证、支付和下载，不在公开站接收密码或创建订单。
- 联系表单目前只做浏览器本地格式校验；后端没有已确认的公开线索收集接口，因此不会伪装成已发送。
- 根域名官网使用独立静态容器，`app.catsco.cc` 继续承载认证工作台；部署前需确认 `catsco.cc`/`www.catsco.cc` 的证书和容器健康状态。
- 隐私政策与使用条款仍是上线前草案，正式发布前需要业务和法律确认。
