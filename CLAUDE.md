# CLAUDE.md

## 1. 项目概览
`zenmind-voice-server` 是一个统一语音服务示例仓库，提供实时 ASR、文本 TTS，以及 ASR -> LLM -> TTS 的 QA 闭环能力。

外层接口契约固定为：

- `GET /api/voice/capabilities`
- `GET /api/voice/tts/voices`
- `GET /api/voice/ws`
- `GET /actuator/health`

## 2. 技术栈
- 后端：Go 1.26、`net/http`、`gorilla/websocket`
- 前端：React 18、TypeScript、Vite
- 部署：Docker、`docker compose`

## 3. 架构设计
- backend 暴露唯一业务前缀 `/api/voice/*`
- console 前端只用于本地调试，不是总控外层路由契约
- QA 模式依赖外部 runner SSE

## 4. 目录结构
- `Makefile`：根目录统一开发命令入口
- `cmd/voice-server`：服务启动入口
- `internal/httpapi`：REST 接口
- `internal/ws`：WebSocket 协议实现
- `frontend`：本地调试控制台
- `compose.yml`：标准 compose 入口

## 5. 数据结构
- `config.App`
- `clientEvent`
- `sessionContext`
- `asrTask` / `ttsTask`

## 6. API 定义
- `GET /api/voice/capabilities`
- `GET /api/voice/tts/voices`
- `GET /api/voice/ws`
- `GET /actuator/health`

## 7. 开发要点
- 默认服务端口仍由 `SERVER_PORT` 控制
- compose 控制台端口由 `FRONTEND_PORT` 控制
- `compose.yml` 同时使用 compose 变量插值和 `voice-server` 的 `env_file: .env`
- 对外路径只维护 `/api/voice/*`
- 当前版本未内建业务鉴权，接入方需在网关或部署层控制访问

## 8. 开发流程
1. `cp .env.example .env`
2. `make run`
3. `make frontend-dev`
4. `make test`
5. `make docker-up`

## 9. 已知约束与注意事项
- 本轮总控只接入 backend API，不公开 console
- 若接入外层网关，不要再在路径层做第二套 voice 业务前缀
