# deploy/agent — 节点部署模板

Core 在「新增节点」时渲染的 compose 模板，注入 `PANEL_URL` / `JOIN_TOKEN`（一次性）。

## 待办

- [x] `docker-compose.yml.tmpl`（草案；与 `core/internal/api` 的 composeSnippet 保持同步）
- [x] Agent Dockerfile（草案）
- [x] Xray-core 二进制：**随镜像打包**，跟踪 **snapshot（prerelease）快照通道**——xhttp 上下行分离、后量子密钥交换等前沿特性只在快照版有，稳定版发布很稀疏。默认构建时解析最新发布版；发布流程仍可显式传入一个 release tag 以复现单次构建。运行时在线升级已实现（M6，见 [`../../docs/xray-upgrade.md`](../../docs/xray-upgrade.md)）：镜像里这一份是**地板**，Core 点名版本后 Agent 会取货、校验 SHA-256、解包到 `$CHIRAL_STATE_DIR/kernels/<版本>/` 并重启进去；起不来就自动回滚到上一个二进制。

**状态**：M1–M7 已落地；M8 目前只有不会接管写入的 3x-ui SHADOW 基础设施。

## 3x-ui SHADOW 隔离验证

生成的 Compose 保持 `direct-xray` 默认值。需要在现有节点主机上验证 3x-ui 且不能中断连接时，
使用 [`../3x-ui-lab/`](../3x-ui-lab/README.md) 的独立 Compose 项目。该项目包含官方 3x-ui、
独立 Core 和 Agent，使用无外部网络的共享回环命名空间、项目专属数据卷和新生成的实验凭证；
不复用生产节点身份，也不接入生产 Core。3x-ui 作为独立进程接受只读观测，不接管生产配置或流量。

[`docker-compose.3x-ui-shadow.yml`](docker-compose.3x-ui-shadow.yml) 仅适用于专用测试节点，
或已明确安排中断的节点。将它叠加到现有 Agent 后执行 `docker compose up`，会因环境变量与挂载
变更而重建 Agent；Agent 退出时会停止其管理的 Xray 子进程，已有连接随之中断。`SHADOW` 的
“只读”仅描述 3x-ui API 调用，不能作为部署过程零中断的保证。要求保留现有连接时，不得对
生产 Agent 应用该 override，也不得直接在宿主机网络中启动实验 3x-ui。

Agent 默认只接受回环地址的 3x-ui API，token 文件必须仅对所属用户可读写。实验环境中的
地址、token 与初始化步骤见隔离项目说明；验证结果须以本次实际检查和退出状态为准。

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
