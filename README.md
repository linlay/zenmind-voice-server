# zenmind-voice-server

## 1. 项目简介
这是一个统一语音服务示例仓库，提供实时 ASR、本地 TTS 和 QA 闭环语音对话能力。默认端口是 `11953`，业务接口统一使用 `/api/voice/` 前缀，适合给其他项目演示和集成。

## 2. 快速开始
### 前置要求
- Go 1.26+
- Node.js 18+
- DashScope 相关凭据
- 可选：支持 SSE 的 runner 服务（QA 模式需要）

### 初始化配置
```bash
cp .env.example .env
```

然后按 `.env.example` 填写本地真实配置。`.env` 不提交仓库。

### 启动后端
```bash
go run ./cmd/voice-server
```

默认地址：
- HTTP：`http://localhost:11953`
- WebSocket：`ws://localhost:11953/api/voice/ws`

### 启动前端
```bash
cd frontend
npm install
npm run dev
```

前端开发服务默认运行在 `http://localhost:5173`。Vite 会把 `/api` 和 `/actuator` 代理到 `http://localhost:11953`，前端默认 WebSocket 地址会自动指向 `ws://localhost:11953/api/voice/ws`。

### 验证
```bash
go test ./...
cd frontend && npm run build
```

## 3. 配置说明
- 环境变量契约文件：`.env.example`
- 本地真实配置文件：`.env`
- 默认配置值主维护位置：`internal/config/config.go`
- 当前仓库通过环境变量覆盖默认值，不在 README 中重复维护详细默认字段

最常用配置：
- `SERVER_PORT=11953`
- `DASHSCOPE_API_KEY`
- `DASHSCOPE_TTS_API_KEY`
- `APP_VOICE_TTS_LLM_RUNNER_BASE_URL`
- `APP_VOICE_TTS_LLM_RUNNER_AGENT_KEY`

说明：
- 仅使用 `.env` / 环境变量注入真实值
- README 不作为配置默认值事实源
- QA 模式依赖 runner；如果未配置，只能使用本地 ASR/TTS
- 协议字段和接口定义请查看 [docs/api/voice-protocol.md](./docs/api/voice-protocol.md)

## 4. 部署
### 构建后端镜像
```bash
docker build -t zenmind-voice-server:local .
```

### 构建前端镜像
```bash
docker build -t zenmind-voice-console:local ./frontend
```

### 单独运行后端容器
```bash
docker run --rm \
  --env-file .env \
  -p 11953:11953 \
  zenmind-voice-server:local
```

### 使用 Docker Compose
```bash
docker compose up --build
```

默认访问地址：
- 前端入口：`http://localhost:8088`
- 后端调试：`http://localhost:11953`
- 健康检查：`http://localhost:11953/actuator/health`

Compose 中：
- `voice-server` 负责 Go 后端
- `voice-console` 负责前端控制台
- 前端 Nginx 会代理 `/api`、`/actuator` 和 `/api/voice/ws` 到 `voice-server:11953`

## 5. 运维
### 健康检查
```bash
curl -sS http://localhost:11953/actuator/health
```

### 查看能力接口
```bash
curl -sS http://localhost:11953/api/voice/capabilities
```

### 常见排查
- 检查 `.env` 是否已填写 DashScope 凭据
- 检查 `SERVER_PORT` 是否被外部环境覆盖
- 检查 QA 模式所需 runner 是否可访问
- 检查前端是否连到了 `ws://localhost:11953/api/voice/ws`
- 检查调用方是否正确处理 `tts.audio.chunk` 后紧跟的 binary 音频帧

### 协议与集成文档
- 对外 API / WebSocket 协议文档： [docs/api/voice-protocol.md](./docs/api/voice-protocol.md)
- 项目事实文档： [CLAUDE.md](./CLAUDE.md)
