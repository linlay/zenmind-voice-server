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
- `voice-server-backend` service 的 `env_file: .env` 属于容器运行时环境注入
- 最重要的部署路径约束是 `/api/voice/*`
- 若由总网关接入，应直接把 `/api/voice/*` 反代到 backend，而不是依赖 console 前端
- LLM QA 模式支持由客户端在 `tts.start.agentKey` 中动态指定员工；`APP_VOICE_TTS_LLM_RUNNER_AGENT_KEY` 仅作为默认回退。
- ASR 本地音量门限默认开启，可通过 `APP_VOICE_ASR_CLIENT_GATE_*` 调整浏览器侧 RMS 门限、开门/关门保持时长和预缓冲时长。
- ASR WebSocket 详细日志开关：`APP_VOICE_ASR_WS_DETAILED_LOG_ENABLED=true|false`
- TTS WebSocket 详细日志开关：`APP_VOICE_TTS_WS_DETAILED_LOG_ENABLED=true|false`
- 详细日志默认关闭；开启后会记录文本、任务元数据和音频字节数，但不会打印音频内容或密钥。

## 4. 部署

```bash
make docker-up
```

- `compose.yml` 是开发场景的标准 compose 入口
- 若同时存在 `compose.yaml` 和 `docker-compose.yml`，Docker 会优先使用 `compose.yaml` 并给出多配置警告；当前已统一为单文件
- `compose.yml` 中的 service 名统一为 `voice-server-backend` 和 `voice-server-frontend`
- Compose 构建后的镜像标签统一为 `voice-server-backend:latest` 和 `voice-server-frontend:latest`
- backend / frontend 的容器名也分别是 `voice-server-backend` 和 `voice-server-frontend`，便于结合 `docker ps` / `docker images` 排查部署状态
- frontend 容器在 compose 网络内通过 service 名 `voice-server-backend` 反代 backend，避免在共享 `zenmind-network` 上使用过于通用的服务发现名
- 若只启动 backend 容器，可使用 `make docker-up-backend`
- compose 打开 frontend 的地址为 `http://localhost:${FRONTEND_PORT}`，默认 `http://localhost:11954`
- 停止 compose 环境：`make docker-down`
- 底层编排命令仍为 `docker compose up --build` / `docker compose down`

## 5. 版本化发布 / 离线部署

正式版本的单一来源是根目录 `VERSION`，格式固定为 `vX.Y.Z`。标准 release 打包入口：

```bash
make release
```

也支持显式指定版本与目标架构：

```bash
make release VERSION=v0.1.0 ARCH=amd64
make release VERSION=v0.1.0 ARCH=arm64
```

- 最终产物输出到 `dist/release/`
- 产物命名规则：`zenmind-voice-server-vX.Y.Z-linux-<arch>.tar.gz`
- 每次构建只产出一个目标架构 bundle
- bundle 内包含预构建 backend / frontend 镜像 tar，部署端不需要源码构建环境

离线部署最小步骤：

```bash
tar -xzf zenmind-voice-server-v0.1.0-linux-amd64.tar.gz
cd zenmind-voice-server
cp .env.example .env
./start.sh
```

- `start.sh` 会按需加载 `images/*.tar`
- `start.sh` 会检查 `zenmind-network`，不存在时自动创建
- release bundle 使用 `compose.release.yml`，与开发用 `compose.yml` 分离
- release bundle 的镜像版本由 `.env` 中 `VOICE_SERVER_VERSION` 控制，打包时会自动写入 `.env.example`
- 详细发布说明见 `docs/versioned-release-bundle.md`

## 6. 运维

- 健康检查：`curl -sS http://localhost:${SERVER_PORT}/actuator/health`
- 能力接口：`curl -sS http://localhost:${SERVER_PORT}/api/voice/capabilities`
