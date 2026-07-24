# deploy/panel — Panel 部署

`core` + 前端 + 可选 `caddy`/`nginx`(TLS) 的 docker-compose。

镜像里**同时打包 Xray 二进制**：面板在存储 / 下发前要用 `xray -test` 校验渲染出的 config，并用它派生 Go 标准库没有的 ML-DSA-65 密钥。与 Agent 镜像同走快照通道。

## 关键环境变量

| 变量 | 必需 | 说明 |
|---|---|---|
| `CHIRAL_ADMIN_TOKEN` | 是 | 管理 API 的 bearer token |
| `CHIRAL_SECRET_KEY` | 强烈建议 | 私钥类变量的静态加密密钥；**不设则明文入库**。`openssl rand -base64 32` |
| `CHIRAL_GRPC_PUBLIC_ADDR` | 是 | Agent 拨回的地址，写进生成的 compose |
| `CHIRAL_XRAY_BIN` | 镜像内已设 | 面板侧 Xray 二进制 |
| `CHIRAL_TLS_CERT` / `_KEY` | 生产必需 | gRPC 端 TLS |

## 待办

- [x] `docker-compose.yml`
- [x] Dockerfile.core（含 Xray；web 前端就绪后并入或单独起容器）
- [ ] `.env` 示例

**状态**：草案，随各里程碑修订。
