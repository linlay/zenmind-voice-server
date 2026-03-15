# 统一语音服务 API / WS 协议文档

## 1. 协议概览
本服务对外提供两类接口：
- REST：用于发现服务能力、查询音色白名单
- WebSocket：用于 ASR、TTS、QA 场景的双向实时通讯

默认端口为 `11953`，业务接口统一使用 `/api/voice/` 前缀。

## 2. 基础地址与端口
- 本地 HTTP 基址：`http://localhost:11953`
- 本地 WebSocket 地址：`ws://localhost:11953/api/voice/ws`
- 健康检查：`http://localhost:11953/actuator/health`

当前版本未内建业务鉴权；生产环境建议通过网关、反向代理或网络边界进行访问控制。

## 3. REST 接口清单
### `GET /api/voice/capabilities`
返回协议能力、默认 ASR 参数、TTS 默认模式、音频格式以及 WebSocket 入口。

响应示例：

```json
{
  "websocketPath": "/api/voice/ws",
  "asr": {
    "configured": true,
    "defaults": {
      "sampleRate": 16000,
      "language": "zh",
      "turnDetection": {
        "type": "server_vad",
        "threshold": 0,
        "silenceDurationMs": 400
      }
    }
  },
  "tts": {
    "modes": ["local", "llm"],
    "defaultMode": "local",
    "speechRateDefault": 1.2,
    "audioFormat": {
      "sampleRate": 24000,
      "channels": 1,
      "responseFormat": "pcm"
    },
    "runnerConfigured": true,
    "voicesEndpoint": "/api/voice/tts/voices"
  }
}
```

### `GET /api/voice/tts/voices`
返回允许使用的音色白名单。

响应示例：

```json
{
  "defaultVoice": "Cherry",
  "voices": [
    {
      "id": "Cherry",
      "displayName": "Cherry",
      "provider": "dashscope",
      "default": true
    }
  ]
}
```

## 4. WebSocket 连接与握手
连接地址：`GET /api/voice/ws`

建立连接后，服务端会先发送一条 `connection.ready` 事件：

```json
{
  "type": "connection.ready",
  "sessionId": "ws-session-1742030000000000000",
  "protocolVersion": "v2",
  "capabilities": {
    "asr": true,
    "tts": true,
    "ttsModes": ["local", "llm"]
  }
}
```

字段说明：
- `sessionId`：服务端分配的连接级 ID
- `protocolVersion`：当前协议版本，固定为 `v2`
- `capabilities`：当前连接可用能力

## 5. 双向通讯模型与时序
一个 WebSocket 连接内可以并行运行多个任务，服务端通过 `taskId` 将上下行消息关联到具体任务。

通讯规则：
- 客户端只发送 text frame，消息体为 JSON
- ASR 音频上行通过 `asr.audio.append.audio` 传 base64 编码的 PCM16LE 数据
- 服务端 TTS 音频下行采用“JSON + binary”成对发送
- `task.started` 表示某个任务已进入运行态
- `task.stopped` 表示某个任务完成、停止或异常结束

典型时序：
1. 客户端连接 `/api/voice/ws`
2. 服务端返回 `connection.ready`
3. 客户端发送 `asr.start` 或 `tts.start`
4. 服务端返回 `task.started`
5. 任务运行过程中持续交换增量事件
6. 服务端返回 `tts.done` 或客户端发送 `*.stop`
7. 服务端返回 `task.stopped`

## 6. 客户端事件定义
所有客户端事件都使用 JSON text frame。

### `asr.start`
启动一个 ASR 任务。

必填字段：
- `type`：固定为 `asr.start`
- `taskId`：客户端生成的任务 ID

可选字段：
- `sampleRate`：采样率，默认 `16000`
- `language`：语言，默认 `zh`
- `turnDetection`：服务端 VAD 参数

示例：

```json
{
  "type": "asr.start",
  "taskId": "asr-demo",
  "sampleRate": 16000,
  "language": "zh",
  "turnDetection": {
    "type": "server_vad",
    "threshold": 0,
    "silenceDurationMs": 400
  }
}
```

失败条件：
- `taskId` 为空
- `taskId` 冲突
- ASR API key 未配置

### `asr.audio.append`
向 ASR 任务追加一段音频。

必填字段：
- `type`：固定为 `asr.audio.append`
- `taskId`
- `audio`：base64 编码的 PCM16LE 数据

失败条件：
- 任务不存在
- `audio` 为空
- `audio` 不是合法 base64
- 事件体或音频体积超过服务端限制

### `asr.audio.commit`
告知服务端提交当前缓冲音频。

必填字段：
- `type`：固定为 `asr.audio.commit`
- `taskId`

### `asr.stop`
停止一个 ASR 任务。

必填字段：
- `type`：固定为 `asr.stop`
- `taskId`

### `tts.start`
启动一个 TTS 任务。

必填字段：
- `type`：固定为 `tts.start`
- `taskId`
- `text`：要播报的文本

可选字段：
- `mode`：`local` 或 `llm`，默认取服务端 `defaultMode`
- `voice`：音色 ID，默认取默认音色
- `speechRate`：语速，默认取能力接口返回值
- `chatId`：QA 模式下用于续接上游 runner 会话

示例：

```json
{
  "type": "tts.start",
  "taskId": "tts-demo",
  "mode": "local",
  "text": "你好，欢迎使用统一语音服务。",
  "voice": "Cherry",
  "speechRate": 1.2
}
```

失败条件：
- `taskId` 为空或冲突
- `text` 为空
- `mode` 非 `local` / `llm`
- 本地 TTS 或 runner 未配置

### `tts.stop`
停止一个 TTS 任务。

必填字段：
- `type`：固定为 `tts.stop`
- `taskId`

## 7. 服务端事件定义
### `task.started`
表示某个任务已经进入运行态。

关键字段：
- `taskId`
- `taskType`：`asr` 或 `tts`
- `mode`：仅 TTS 任务存在

### `task.stopped`
表示任务已经停止。

关键字段：
- `taskId`
- `taskType`
- `reason`

### `error`
表示连接级或任务级错误。

关键字段：
- `taskId`：若为空表示连接级输入错误
- `code`
- `message`

### `asr.speech.started`
表示上游 VAD 已检测到语音开始。

### `asr.text.delta`
ASR 增量文本。

关键字段：
- `taskId`
- `text`
- `upstreamType`

### `asr.text.final`
ASR 最终文本。

关键字段：
- `taskId`
- `text`
- `upstreamType`

### `tts.audio.format`
在首个音频块前发送，声明接下来 binary 音频的格式。

关键字段：
- `taskId`
- `sampleRate`
- `channels`
- `voice`
- `voiceDisplayName`

### `tts.text.delta`
QA 模式下 runner 持续返回的文本增量。

### `tts.chat.updated`
QA 模式下 runner 返回的新 `chatId`。

### `tts.audio.chunk`
用于声明紧随其后的 binary frame 元信息。

关键字段：
- `taskId`
- `seq`
- `byteLength`

### `tts.done`
表示 TTS 文本和音频输出完成。

关键字段：
- `taskId`
- `reason`

## 8. Binary 音频帧配对规则
服务端在发送 TTS 音频时严格按以下顺序输出：

1. 一条 JSON 事件：

```json
{
  "type": "tts.audio.chunk",
  "sessionId": "ws-session-1742030000000000000",
  "taskId": "tts-demo",
  "seq": 1,
  "byteLength": 4096
}
```

2. 紧接着发送一条 WebSocket binary frame，内容即该段 PCM16LE 音频数据

客户端必须按顺序消费，不能脱离前一条 `tts.audio.chunk` 单独解释 binary frame。

## 9. 错误码、停止原因与约束
当前版本使用的主要错误码：
- `bad_request`：请求字段不合法、JSON 解析失败或事件类型不支持
- `task_conflict`：`taskId` 已被当前连接占用
- `config_missing`：缺少 API key 或 runner 配置
- `task_not_found`：目标任务不存在或已结束
- `event_too_large`：单条客户端事件过大
- `audio_too_large`：音频字段过大
- `proxy_client_queue_full`：ASR 上游未就绪时，排队事件超限
- `upstream_error`：上游 ASR / TTS 服务异常
- `connect_failed`：ASR 上游连接建立失败
- `runner_failed`：runner SSE 失败
- `tts_failed`：TTS 会话失败

常见 `reason`：
- `client_stop`
- `completed`
- `no_content`
- `connection_closed`
- `upstream_finished`
- `upstream_error`
- `runner_failed`
- `tts_failed`
- `proxy_client_queue_full`

约束说明：
- 当前只支持客户端 text frame，不支持客户端 binary frame
- `taskId` 在单个连接内必须唯一
- QA 模式依赖 runner SSE；未配置时只能使用本地 ASR/TTS

## 10. 集成示例与接入建议
推荐接入流程：
1. 调用 `GET /api/voice/capabilities` 获取 `websocketPath`、默认音频参数和 TTS 模式
2. 调用 `GET /api/voice/tts/voices` 拉取音色白名单
3. 建立 WebSocket 连接，等待 `connection.ready`
4. 根据场景发送 `asr.start` 或 `tts.start`
5. 按 `taskId` 跟踪任务生命周期，并正确处理 `tts.audio.chunk` + binary 配对

JavaScript 连接示例：

```js
const socket = new WebSocket("ws://localhost:11953/api/voice/ws");
socket.binaryType = "arraybuffer";

socket.onmessage = (event) => {
  if (typeof event.data === "string") {
    const message = JSON.parse(event.data);
    console.log("json event", message);
    return;
  }

  console.log("binary audio frame", event.data.byteLength);
};

socket.onopen = () => {
  socket.send(JSON.stringify({
    type: "tts.start",
    taskId: "tts-demo",
    mode: "local",
    text: "你好，欢迎接入统一语音服务。",
    voice: "Cherry"
  }));
};
```
