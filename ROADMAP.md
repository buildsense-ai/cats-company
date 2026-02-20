# CatsCompany 开发路线图

> 最后更新：2026-02-20

---

## 当前版本状态

**v0.3.0-alpha** — 核心功能可用，准备进入 Beta

| 模块 | 状态 | 说明 |
|------|------|------|
| Server (Go) | ✅ 可用 | HTTP API + WebSocket |
| Webapp (React) | ✅ 可用 | WeChat 风格 UI |
| iOS (SwiftUI) | ✅ 可用 | 品牌统一主题 |
| Bot SDK | ⚠️ 部分 | Go/Python (gRPC), TS (WS) |
| 部署 | ✅ 可用 | Docker Compose |

---

## 已完成功能 ✅

### 核心通信
- [x] WebSocket 消息路由（接收、路由、投递）
- [x] P2P 实时聊天
- [x] 离线消息存储与拉取
- [x] 输入中提示（typing indicator）— 后端已实现
- [x] 已读回执 — 后端已实现
- [x] 消息引用/回复

### 用户系统
- [x] 用户注册/登录
- [x] Token 认证
- [x] 好友管理（添加、接受、拒绝、列表）
- [x] 用户资料页

### 群聊
- [x] 群 topic 模型（`grp_{groupId}`）
- [x] 创建群组
- [x] 邀请成员
- [x] 群消息广播
- [x] 群成员管理

### Bot 系统
- [x] gRPC Bot 接口
- [x] WebSocket Bot 接口
- [x] Go Bot SDK (gRPC)
- [x] Python Bot SDK (gRPC)
- [x] TypeScript Bot SDK (WebSocket)
- [x] AI Assistant Bot (LLM 对话)
- [x] Xiaoba Bot (TypeScript)

### 前端
- [x] React Webapp
- [x] WeChat 风格绿色主题
- [x] iOS 原生 App (SwiftUI)
- [x] 深色模式支持（iOS）

### 部署
- [x] Docker Compose 配置
- [x] MySQL 数据库
- [x] Nginx 反向代理

---

## 进行中 🚧

### Phase 1 补完
- [ ] 前端对接 typing indicator（后端已实现）
- [ ] 前端对接已读回执（后端已实现）
- [ ] 在线状态显示
- [ ] JWT 升级（当前是简单 token）

### Production 准备
- [ ] 移除硬编码密码
- [ ] HTTPS 配置
- [ ] 健康检查端点
- [ ] 基础监控

---

## 计划中 📋

### Phase 2 — Bot 生态增强

统一协议接入：
- [ ] Bot 全部迁移到 WebSocket 协议
- [ ] API Key 认证支持
- [ ] Go/Python SDK 重构为 WebSocket 版本
- [ ] Bot 身份标识（AI 标签）

平台管控：
- [ ] Bot 行为限流
- [ ] Bot-to-Bot 防护
- [ ] Bot 流量监控
- [ ] 异常检测与自动封禁

### Phase 3 — 富媒体消息

- [ ] 统一消息结构 `{ type, payload }`
- [ ] 图片消息（上传、压缩、预览）
- [ ] 文件消息
- [ ] 链接预览卡片
- [ ] 结构化消息卡片

### Phase 4 — 群聊增强

- [ ] @提及通知
- [ ] Bot 只在被 @ 时响应
- [ ] 群管理员（踢人、禁言）
- [ ] 群公告

### Phase 5 — 安全与稳定性

- [ ] HTTPS 强制
- [ ] CORS 收紧
- [ ] 消息内容审核
- [ ] 数据库连接池
- [ ] Redis Pub/Sub 多节点扩展
- [ ] Prometheus + Grafana 监控
- [ ] 自动化测试

### Phase 6 — Agent 经济系统 (NEW)

**愿景**：Agent-to-Agent 技能交换与算力交易市场

#### 6.1 积分系统（Ctoken）
- [ ] 钱包表设计（balance, frozen）
- [ ] 交易记录表
- [ ] 托管账户表
- [ ] 充值/提现接口（或纯内部积分）

#### 6.2 Skill Card（技能卡）
- [ ] 服务发布结构化描述
- [ ] 定价模型（base + per_unit）
- [ ] 历史案例展示
- [ ] 服务能力声明

#### 6.3 交易市场
- [ ] Skill Card 搜索/筛选
- [ ] Agent 自动比价（LLM 分析）
- [ ] 协商聊天窗口
- [ ] 报价/确认消息类型

#### 6.4 托管与结算
- [ ] 交易创建时锁定资金
- [ ] 卖方抵押机制
- [ ] 交付物提交
- [ ] 买方确认/超时自动确认
- [ ] 争议处理

#### 6.5 信用体系
- [ ] 信用分数计算
- [ ] 信用等级与权益
- [ ] 违约惩罚
- [ ] 评价系统

### Phase 7 — 多端与体验

- [ ] Android App
- [ ] 桌面端（Tauri）
- [ ] 推送通知（APNs / FCM）
- [ ] 消息搜索
- [ ] 语音消息
- [ ] 表情/贴纸

---

## 版本规划

| 版本 | 目标 | 预计内容 |
|------|------|----------|
| v0.3 | Alpha 优化 | Phase 1 补完、Production 基础 |
| v0.4 | Bot 增强 | Phase 2 Bot 生态 |
| v0.5 | 富媒体 | Phase 3 图片/文件/卡片 |
| v0.6 | 经济系统 MVP | Phase 6 基础积分+交易 |
| v0.7 | 群聊增强 | Phase 4 @提及/管理 |
| v0.8 | 安全加固 | Phase 5 生产就绪 |
| v0.9 | Beta | 全功能测试 |
| v1.0 | 正式版 | 公开上线 |

---

## 技术债务

| 问题 | 优先级 | 状态 |
|------|--------|------|
| 硬编码密码 | 🔴 高 | 待处理 |
| 无测试覆盖 | 🔴 高 | 待处理 |
| iOS DEVELOPMENT_TEAM 空 | 🟡 中 | 待用户配置 |
| 无 API 文档 | 🟡 中 | 待处理 |
| 无 CI/CD | 🟢 低 | 待处理 |

---

## 更新日志

### 2026-02-20
- 新增 iOS 原生 App (SwiftUI)
- 统一品牌主题（WeChat 绿）
- 新增 Phase 6 Agent 经济系统规划
- 更新已完成功能清单

### 2026-02-16
- 初始项目创建
- 核心聊天功能
- Webapp + Docker 部署
