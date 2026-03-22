# 版本化离线打包方案

## 1. 目标与边界

这套方案的目标，是把 `zenmind-voice-server` 产出成一个带明确版本号、单目标架构、可离线部署的 release bundle，便于上传到 GitHub Release、自建制品库或内网服务器，再由部署端直接解压运行。

它解决的是“如何交付可运行版本”，不是“如何分发源码”或“如何把第三方依赖一起离线化”：

- 交付物是最终 bundle，而不是源码压缩包。
- bundle 内包含预构建 backend / frontend 镜像和最小部署资产。
- 部署端不需要源码构建环境。
- 每次构建只产出一个目标架构 bundle，不做多架构合包。
- `.env` 中配置的 ASR / TTS / runner 等外部服务仍需部署端自行可达。

当前仓库的版本单一来源是根目录 `VERSION` 文件，正式版本格式固定为 `vX.Y.Z`。以 `v0.1.0` 为例，最终产物命名规则为：

- `zenmind-voice-server-v0.1.0-linux-arm64.tar.gz`
- `zenmind-voice-server-v0.1.0-linux-amd64.tar.gz`

## 2. 方案总览

从可复用角度看，这套方案仍拆成四层：

1. 版本层：根目录 `VERSION` 统一管理版本号。
2. 构建层：按目标架构构建 backend 和 frontend release 镜像。
3. 组装层：把镜像 tar、release compose、启动脚本、配置模板一起组装成离线目录。
4. 交付层：把离线目录压缩成最终 bundle，输出到固定产物目录。

当前仓库的对应位置为：

- 版本来源：`VERSION`
- 构建入口：`make release` / `scripts/release.sh`
- 模板资产：`scripts/release-assets/`
- 最终产物目录：`dist/release/`

## 3. 当前项目怎么打包

### 3.1 打包入口

标准正式发布入口：

```bash
make release
```

`Makefile` 会把 `VERSION` 和 `ARCH` 传给 `scripts/release.sh`：

```bash
VERSION=$(VERSION) ARCH=$(ARCH) bash scripts/release.sh
```

也可以直接执行脚本：

```bash
bash scripts/release.sh
```

常见用法：

```bash
make release VERSION=v1.0.0 ARCH=arm64
make release VERSION=v1.0.0 ARCH=amd64
```

其中：

- `VERSION` 默认读取根目录 `VERSION`
- `ARCH` 未显式传入时，会按 `uname -m` 自动识别为 `amd64` 或 `arm64`
- 脚本内部会把 `ARCH` 转成 `linux/<arch>` 作为 Docker buildx 的目标平台

### 3.2 打包输入

`scripts/release.sh` 的主要输入包括：

- 版本号：`VERSION` 文件或环境变量 `VERSION`
- 目标架构：环境变量 `ARCH` 或当前机器架构
- backend 容器构建定义：`Dockerfile`
- frontend 容器构建定义：`frontend/Dockerfile`
- release 模板资产：`scripts/release-assets/docker-compose.release.yml`
- release 模板资产：`scripts/release-assets/start.sh`
- release 模板资产：`scripts/release-assets/stop.sh`
- release 模板资产：`scripts/release-assets/README.txt`
- 配置模板：`.env.example`

脚本会强校验版本格式：

- 只接受 `vX.Y.Z`
- 不符合时直接失败，不继续构建

### 3.3 构建过程

打包脚本先构建两个 release 镜像：

- 后端镜像：`voice-server-backend:<VERSION>`
- 前端镜像：`voice-server-frontend:<VERSION>`

镜像由 `docker buildx build` 构建，并直接导出为 Docker 镜像 tar：

```bash
docker buildx build \
  --platform "linux/$ARCH" \
  --file Dockerfile \
  --tag "voice-server-backend:$VERSION" \
  --output "type=docker,dest=.../voice-server-backend.tar" \
  .
```

```bash
docker buildx build \
  --platform "linux/$ARCH" \
  --file frontend/Dockerfile \
  --tag "voice-server-frontend:$VERSION" \
  --output "type=docker,dest=.../voice-server-frontend.tar" \
  frontend
```

其中 backend Dockerfile 使用 buildx 提供的 `TARGETOS` / `TARGETARCH` 编译 Go 二进制，避免把 release 包固定打成 `amd64`。

### 3.4 组装过程

镜像构建完成后，脚本会在临时目录组装一个标准离线目录 `zenmind-voice-server/`，并拷贝以下内容：

- `images/voice-server-backend.tar`
- `images/voice-server-frontend.tar`
- `docker-compose.release.yml`
- `start.sh`
- `stop.sh`
- `README.txt`
- `.env.example`

同时脚本会把 bundle 内 `.env.example` 的 `VOICE_SERVER_VERSION` 改成当前构建版本，保证部署端复制后默认镜像标签与 bundle 内镜像一致。

### 3.5 最终输出

最终会压缩成：

```text
dist/release/zenmind-voice-server-vX.Y.Z-linux-<arch>.tar.gz
```

这就是对外分发的正式交付物。

## 4. 打哪些包，产物在哪里

### 4.1 镜像层产物

镜像层产物位于 bundle 的 `images/` 目录：

- `images/voice-server-backend.tar`
- `images/voice-server-frontend.tar`

它们不是最终对外分发文件，但它们是 bundle 的核心内容。部署端如果本机还没有对应标签镜像，`start.sh` 会自动执行 `docker load`。

### 4.2 交付层产物

交付层产物只有最终离线 bundle：

- `dist/release/zenmind-voice-server-vX.Y.Z-linux-arm64.tar.gz`
- `dist/release/zenmind-voice-server-vX.Y.Z-linux-amd64.tar.gz`

注意：

- 每次构建只会产出其中一个架构包
- `dist/release/` 是固定输出目录，适合做上传、归档和校验入口
- `dist/` 已加入 `.gitignore`

### 4.3 bundle 解压后的结构

bundle 解压后目录如下：

```text
zenmind-voice-server/
  .env.example
  docker-compose.release.yml
  start.sh
  stop.sh
  README.txt
  images/
    voice-server-backend.tar
    voice-server-frontend.tar
```

运行时还会在本地生成：

- `.env`：由使用者从 `.env.example` 复制并填入真实配置

其中最重要的职责分别是：

- `.env`：控制版本号、端口以及外部 ASR / TTS / runner 访问参数
- `images/`：保存预构建镜像 tar，保证部署端不用重新 build

## 5. 部署端如何消费这些包

### 5.1 标准部署步骤

部署端拿到 bundle 后，最小步骤是：

```bash
tar -xzf zenmind-voice-server-v1.0.0-linux-amd64.tar.gz
cd zenmind-voice-server
cp .env.example .env
./start.sh
```

### 5.2 `start.sh` 做了什么

`start.sh` 是离线 bundle 的实际启动入口，会按顺序完成：

1. 校验 `.env` 是否存在。
2. 校验宿主机上有 Docker Engine 和 docker compose v2。
3. 从 `.env` 读取 `VOICE_SERVER_VERSION`。
4. 计算要启动的镜像标签：
   - `voice-server-backend:$VOICE_SERVER_VERSION`
   - `voice-server-frontend:$VOICE_SERVER_VERSION`
5. 如果本机没有这些镜像，就从 `images/*.tar` 自动执行 `docker load`。
6. 检查 `zenmind-network` 是否存在；不存在时自动创建。
7. 执行：

```bash
docker compose -f docker-compose.release.yml up -d
```

### 5.3 `stop.sh` 做了什么

`stop.sh` 会读取同一份 `.env` 中的 `VOICE_SERVER_VERSION`，然后执行：

```bash
docker compose -f docker-compose.release.yml down --remove-orphans
```

它只负责停止当前 release compose 环境，不会删除镜像 tar 或 bundle 文件。
