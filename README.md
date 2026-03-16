# zenmind-voice-server

## 1. 项目简介

这是一个统一语音服务示例仓库，提供实时 ASR、本地 TTS 和 QA 闭环语音对话能力。

外层网关契约已经固定为：

- 业务接口统一走 `/api/voice/*`
- 健康检查走 `/actuator/health`
- 本轮总控只接入 backend API，不公开 voice console 路由

## 2. 快速开始

### 前置要求

- Go 1.26+
- Node.js 22+ 与 npm
- Docker / Docker Compose（按需使用容器启动）

### 本地只启动 backend

```bash
cp .env.example .env
make run
```

默认地址（当 `SERVER_PORT=11953` 时）：

- HTTP：`http://localhost:11953`
- WebSocket：`ws://localhost:11953/api/voice/ws`

### 本地启动 frontend 调试台

```bash
cp .env.example .env
make frontend-install
make frontend-dev
```

- frontend 本地访问地址：`http://localhost:5173`
- frontend 通过 Vite 代理访问根 `.env` 中 `SERVER_PORT` 指定的 backend

### 测试

```bash
make test
```

## 3. 配置说明

- 环境变量契约文件：`.env.example`
- `.env` 是端口与密钥的单一事实源，当前至少维护 `SERVER_PORT` 与 `FRONTEND_PORT`
- `compose.yml` 里的 `${SERVER_PORT}` / `${FRONTEND_PORT}` 属于 compose 变量插值
- `voice-server` 的 `env_file: .env` 属于容器运行时环境注入
- 最重要的部署路径约束是 `/api/voice/*`
- 若由总网关接入，应直接把 `/api/voice/*` 反代到 backend，而不是依赖 console 前端

## 4. 部署

```bash
make docker-up
```

- `compose.yml` 是唯一标准 compose 入口
- 若同时存在 `compose.yaml` 和 `docker-compose.yml`，Docker 会优先使用 `compose.yaml` 并给出多配置警告；当前已统一为单文件
- `voice-server` 负责 backend
- `voice-console` 仅用于本地调试控制台
- compose 打开 frontend 的地址为 `http://localhost:${FRONTEND_PORT}`，默认 `http://localhost:8088`
- 停止 compose 环境：`make docker-down`
- 底层编排命令仍为 `docker compose up --build` / `docker compose down`

## 5. 运维

- 健康检查：`curl -sS http://localhost:${SERVER_PORT}/actuator/health`
- 能力接口：`curl -sS http://localhost:${SERVER_PORT}/api/voice/capabilities`
