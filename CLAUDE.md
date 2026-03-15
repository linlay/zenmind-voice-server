# CLAUDE.md

## 1. 项目概览
`zenmind-voice-server` 是一个统一语音服务示例仓库，提供实时 ASR、文本 TTS，以及 ASR -> LLM -> TTS 的 QA 闭环能力。对外业务接口统一使用 `/api/voice/` 前缀，默认服务端口为 `11953`。

## 2. 技术栈
- 后端：Go 1.26、`net/http`、`gorilla/websocket`
- 前端：React 18、TypeScript、Vite
- 协议：HTTP JSON、WebSocket、SSE、binary PCM 音频帧
- 部署：Docker、Docker Compose、前端 Nginx 反向代理

## 3. 架构设计
系统分为三个层次：
- HTTP/WS 接入层：负责暴露 `/api/voice/capabilities`、`/api/voice/tts/voices`、`/api/voice/ws` 和 `/actuator/health`
- 语音编排层：在单条 WebSocket 连接内通过 `taskId` 并行管理 ASR 与 TTS 任务生命周期
- 上游集成层：ASR 和本地 TTS 通过 DashScope realtime WebSocket 接入，QA 模式通过 runner SSE 获取 LLM 文本增量

## 4. 目录结构
- `cmd/voice-server`：服务启动入口
- `internal/config`：默认配置和环境变量加载
- `internal/httpapi`：REST 接口
- `internal/ws`：WebSocket 协议实现与任务编排
- `internal/asr`：ASR 上游连接
- `internal/tts`：TTS 会话、音色目录和格式处理
- `internal/runner`：QA 模式的 runner SSE 客户端
- `frontend`：控制台前端
- `docs/api`：对外协议文档

## 5. 数据结构
核心运行时结构：
- `config.App`：服务端口、ASR、TTS 和 runner 配置
- `clientEvent`：客户端发往 WebSocket 的统一事件载体
- `sessionContext`：单个 WebSocket 连接上下文，维护 `sessionId`、活跃任务和发送锁
- `asrTask` / `ttsTask`：任务级状态、上游连接、播放序号和停止状态
- `core.AudioChunk`：TTS 下行的音频块，配对输出 JSON 元信息和 binary PCM 数据

## 6. API 定义
官方业务接口：
- `GET /api/voice/capabilities`
- `GET /api/voice/tts/voices`
- `GET /api/voice/ws`

运维接口：
- `GET /actuator/health`

协议细节、字段定义、报文样例和接入说明统一维护在 [docs/api/voice-protocol.md](./docs/api/voice-protocol.md)。

## 7. 开发要点
- 默认端口由 `internal/config/config.go` 维护，环境变量契约由 `.env.example` 维护
- `.env` 仅用于本地真实值，不提交仓库
- 业务接口路径以代码实现和协议文档为准，README 不重复维护字段默认值
- WebSocket 客户端只发送 text frame；服务端 `tts.audio.chunk` 后必须紧跟一帧 binary 音频
- 当前版本未内建业务鉴权，接入方需通过网关、网络边界或部署侧控制访问

## 8. 开发流程
- 复制配置：`cp .env.example .env`
- 启动后端：`go run ./cmd/voice-server`
- 启动前端：`cd frontend && npm install && npm run dev`
- 后端验证：`go test ./...`
- 前端验证：`cd frontend && npm run build`
- 容器运行：`docker compose up --build`

## 9. 已知约束与注意事项
- QA 模式依赖外部 runner 的 SSE 接口；未配置时仅本地 ASR/TTS 可用
- 前端开发服务默认跑在 `5173`，通过 Vite 代理访问后端 `11953`
- 当前仓库未提供持久化存储，`chatId` 仅作为上游 runner 上下文标识透传
- 如果后续需要兼容旧路径，应单独设计过渡期，不在当前版本文档中双轨维护
