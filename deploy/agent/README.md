# deploy/agent — 节点部署模板

Core 在「新增节点」时渲染的 compose 模板，注入 `PANEL_URL` / `JOIN_TOKEN`（一次性）。

## 待办

- [x] `docker-compose.yml.tmpl`（草案；与 `core/internal/api` 的 composeSnippet 保持同步）
- [x] Agent Dockerfile（草案）
- [x] Xray-core 二进制：**随镜像打包**，跟踪 **snapshot（prerelease）快照通道**——xhttp 上下行分离、后量子密钥交换等前沿特性只在快照版有，稳定版发布很稀疏。默认构建时拉最新快照；`--build-arg XRAY_VERSION=v26.7.11` 可 pin 具体版本以求可复现。运行时在线升级已实现（M6，见 [`../../docs/xray-upgrade.md`](../../docs/xray-upgrade.md)）：镜像里这一份是**地板**，Core 点名版本后 Agent 会取货、校验 SHA-256、解包到 `$CHIRAL_STATE_DIR/kernels/<版本>/` 并重启进去；起不来就自动回滚到上一个二进制。

**状态**：M1–M6 已落地并真机验证过（注册、配置下发、在线增删用户、内核升级与回滚）。

## 数据卷（不能省）

`/var/lib/chiral-agent`（`CHIRAL_STATE_DIR`）里放着三样东西：

1. **节点身份**（`state.json`）——丢了要拿新的 join token 重新注册。
2. **当前应用的 config.json**——丢了在重连上 Core 之前这台节点不服务。
3. **升级装好的内核** + `kernels/active` 指针。

第 3 条是 M6 新增的，也是最容易忽略的：`active` 记录着当前跑的是哪个版本。
没有这个卷，容器一重启就悄悄退回镜像里烘焙的那个 Xray，而面板那边仍然认为
这台节点是升级过的——**一次没人察觉的降级**。

Agent 启动时会验证这个指针：版本得真的装着，而且那个二进制自己报出来的版本
要对得上；任一条不满足就退回镜像里的那份。指向一个不存在的路径会起不来，
指向一个标错的 build 会悄悄跑一个没人点名过的版本，两者都比没有指针更糟。
