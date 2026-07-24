# proto — Core↔Agent gRPC 定义

Core 与 Agent **共享**的 protobuf 定义与生成代码。是两个组件间唯一的共享面（不互相 import 对方 `internal/`）。协议语义见 [`../docs/communication.md`](../docs/communication.md)。

## 服务（已定稿）

定义在 [`chiral/v1/agent.proto`](chiral/v1/agent.proto)：

- **`Register`（unary）**：一次性 join token 换每节点长期凭证。
- **`Channel`（双向流）**：凭证放 metadata `x-chiral-credential`，一条流承载所有帧。
  - Agent → Core：`Hello`（首帧）、`Heartbeat`、`StatsReport`*、`ConfigAck`、`Event`
  - Core → Agent：`ConfigPush`、`UserOp`*（AddUser/RemoveUser）、`Command`（重启 / 立即上报）

\* 标注的帧 M3 才实现，字段为占位草案。

## 生成（已定：buf + 生成物入库）

```bash
buf lint && buf generate
```

- 工具链 **buf**（根目录 `buf.yaml` / `buf.gen.yaml`，remote 插件，无需本地 protoc）。
- 生成的 `*.pb.go` **提交入库**：克隆即可 `go build`。改动 proto 后必须重新生成并一起提交，CI 校验两者同步。
