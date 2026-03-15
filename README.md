# zenmind-voice-server-go

Go 1.26 版统一语音服务示例，复刻自隔壁的 `zenmind-voice-server` Java 项目：

- 测试模式：独立验证实时 ASR 与本地文本 TTS
- QA 模式：运行完整的 ASR -> LLM -> TTS 连续对话链路

两种能力共用同一条 WebSocket 连接：`/ws/voice`。连接内通过 `taskId` 区分任务，`asr` 与 `tts` 互不干扰。

## 环境要求

- Go 1.26+
- Node.js 18+

## 初始化

```bash
cp .example.env .env
```

服务会在启动时从仓库根目录自动加载 `.env`。

TTS realtime 的 `DASHSCOPE_TTS_RESPONSE_FORMAT` 使用上游原生枚举：`pcm`、`wav`、`mp3`、`opus`。当前前端默认按 `pcm + 24000Hz + mono` 播放。

## 后端启动

```bash
go run ./cmd/voice-server
```

默认地址：

- HTTP: `http://localhost:8080`
- WebSocket: `ws://localhost:8080/ws/voice`

## 前端启动

```bash
cd frontend
npm install
npm run dev
```

Vite 会把 `/api`、`/actuator` 代理到 `http://localhost:8080`。开发环境中的业务 WebSocket 仍会直接连接 `ws://localhost:8080/ws/voice`。

LLM TTS 使用的 runner 需提供 `POST /api/query` SSE 接口；`APP_VOICE_TTS_LLM_RUNNER_BASE_URL` 只填写服务根地址，例如 `http://localhost:8081`。

## Docker 镜像

### 构建后端镜像

```bash
docker build -t zenmind-voice-server-go:local .
```

### 构建前端镜像

```bash
docker build -t zenmind-voice-console:local ./frontend
```

### 仅启动后端容器

```bash
docker run --rm \
  --env-file .env \
  -p 8080:8080 \
  zenmind-voice-server-go:local
```

后端健康检查地址仍然是 `http://localhost:8080/actuator/health`。

## Docker Compose

推荐把前后端一起通过 compose 启动：

```bash
docker compose up --build
```

默认访问地址：

- 前端入口：`http://localhost:8088`
- 后端调试：`http://localhost:8080`
- 后端健康检查：`http://localhost:8080/actuator/health`

compose 中：

- `voice-server` 服务独立构建 Go 后端镜像
- `voice-console` 服务独立构建前端镜像，并通过 Nginx 代理 `/api`、`/actuator`、`/ws/voice` 到 `voice-server:8080`

外部 LLM runner 不包含在 compose 中，仍通过 `.env` 里的 `APP_VOICE_TTS_LLM_RUNNER_BASE_URL` 等环境变量接入。

## REST API

### `GET /api/voice/capabilities`

返回当前协议能力、默认参数和 TTS 运行模式。

### `GET /api/tts/voices`

返回允许使用的音色白名单。

## WebSocket 协议

地址：`/ws/voice`

连接建立后服务端先发：

```json
{
  "type": "connection.ready",
  "sessionId": "ws-session-uuid",
  "protocolVersion": "v2",
  "capabilities": {
    "asr": true,
    "tts": true,
    "ttsModes": ["local", "llm"]
  }
}
```

客户端事件：

- `asr.start`
- `asr.audio.append`
- `asr.audio.commit`
- `asr.stop`
- `tts.start`
- `tts.stop`

服务端事件：

- `task.started`
- `task.stopped`
- `error`
- `asr.speech.started`
- `asr.text.delta`
- `asr.text.final`
- `tts.audio.format`
- `tts.text.delta`
- `tts.chat.updated`
- `tts.audio.chunk`
- `tts.done`

`tts.audio.chunk` 之后紧跟一条 WebSocket binary message，对应同一段 PCM 音频。

## Go 实现说明

阿里云目前没有现成的 Go SDK 能直接替代 Java 版里使用的 DashScope realtime SDK，所以这个项目改为直接实现底层协议：

- DashScope realtime ASR：Go 直接连 WebSocket 协议
- DashScope realtime TTS：Go 直接连 WebSocket 协议
- Runner：Go 直接解析 SSE 流

对前端暴露的 REST 和 WebSocket 契约保持与 Java 版一致，前端业务逻辑不需要因为后端从 Java 改到 Go 而调整。
# zenmind-voice-server
