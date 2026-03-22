zenmind-voice-server - 离线部署包

本文件只说明 bundle 解压后的最小操作。仓库级发布流程、版本约束和产物说明请查看源码仓库 README 与 docs/versioned-release-bundle.md。

部署步骤
========

1. 复制 .env.example 为 .env，并填入真实配置。
2. 运行 ./start.sh 启动服务。
3. 浏览器访问 http://127.0.0.1:11954/ （实际端口取决于 .env 中的 FRONTEND_PORT）。
4. 运行 ./stop.sh 停止服务。

目录说明
========

.env.example               - 环境变量模板
docker-compose.release.yml - 容器编排
start.sh                   - 启动脚本（会按需加载 images/*.tar，并确保 zenmind-network 存在）
stop.sh                    - 停止脚本
README.txt                 - 本文件
images/                    - Docker 镜像 tar 文件

注意事项
========

- 需要 Docker Engine 20+ 和 docker compose v2。
- .env 中的 VOICE_SERVER_VERSION 必须与镜像标签一致；打包产物中的 .env.example 已默认写入当前版本。
- 本 bundle 离线交付的是应用镜像与部署资产；ASR / TTS / runner 等外部依赖仍需按 .env 配置可访问。
