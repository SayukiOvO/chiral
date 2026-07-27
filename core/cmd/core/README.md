# cmd/core — Core 入口

`main` 包。读 flag / 环境变量 → 打开 SQLite（自动迁移）→ 装配各 `internal` 服务 → 同时起 gRPC server（对 Agent）与 HTTP server（对前端 / 订阅 / 门户），另跑一个 60 秒的后台清扫，收到 SIGINT / SIGTERM 后退出。

## 启动时

- **首个管理员**：库里一个管理员都没有时创建 superadmin。`CHIRAL_ADMIN_USER` / `CHIRAL_ADMIN_PASSWORD` 供自动化部署用；没给密码就随机生成一个并**只打印这一次**——正是为了它不会躺在某个 env 文件里。放在这里而不是做安装向导，是为了一份 compose 文件就能跑起一个可用的新部署。
- **`CHIRAL_ADMIN_TOKEN` 只从环境读，永远不做成 flag**：命令行会经 `ps` 和 shell history 泄露。没设则本次运行随机生成一个并告警。它与管理员账号并存，作为 break-glass 通道，所以这套引导不可能把人锁在外面。
- **门户闸门是拒绝启动，不是告警**：`CHIRAL_PORTAL_MODE` 开着而 `CHIRAL_SECRET_KEY` 为空时直接报错退出。门户按设计要能把订阅 token 读回来展示给用户，明文存意味着「一份 chiral.db 就是每个订阅者的可用链接」。告警会滚过去，而这件事事后无法补救。
- **其余缺配置只是降级并告警**：没有 `CHIRAL_SECRET_KEY`（私钥明文入库）、没有 TLS 证书（gRPC 明文）、没有 `CHIRAL_XRAY_BIN`（跳过面板侧校验与 ML-DSA-65）、没有 SMTP（不提供邮件因子）、public URL 撑不起 WebAuthn（不提供 passkey，而不是提供了在最后一步失败）。
- **`CHIRAL_ONLINE_RECORD` 默认关闭**：这是唯一会记录「用户从哪里连上来」的功能，该由人决定而不是默认开。开启后每 30 秒轮询一轮，这个间隔本身就是采样误差。

## 60 秒清扫

配额与到期在清扫里对账，而不是在流量路径上：用量是在两次清扫之间到达的，所以超额的人在下一轮被切断，而不是在某个请求中途。同一轮里顺手裁掉过期的 history / session / challenge / 审计 / 设备记录与限流计数，并由 `alert` 从**扫出来的状态**宣告节点上下线——因此它能扛面板重启，debounce 也是白送的。

过期的 challenge 不只是垃圾：它占着主键，而好几类 challenge 是按稳定的东西做键的，所以「再给我发一个码」撞上的就是那行陈旧记录。

## 退出

先 `mgr.CloseAll()` 再 `GracefulStop()`。Agent 的 `Channel` 是长期不结束的双向流，只调 `GracefulStop` 会永远等下去；10 秒后仍未停就硬停兜底。
