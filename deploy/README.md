# deploy — 部署

Docker / docker-compose 部署文件。详见 [`../docs/deployment.md`](../docs/deployment.md)。

- `panel/` — Panel 的 `docker-compose.yml` 与 `Dockerfile.core`：多阶段构建 Core、抓 Xray 二进制、`npm run build` 出两个前端入口，合成一个镜像。环境变量清单见 [`panel/README.md`](panel/README.md)
- `agent/` — 节点侧 `Dockerfile` 与 `docker-compose.yml.tmpl`。**这份 tmpl 是给人读的参考件**：Core 并不读它，「新增节点」返回的 compose 片段是在 `core/internal/api/api.go` 里用 Go 拼的，两者需要人工保持一致（那处有注释提醒）

## 防火墙后构建

构建期要出网三次：Go 模块、npm、GitHub（拉 Xray 二进制）。宿主 shell 里的代理**不会**进入构建容器——第一次在国内网络构建时就栽在这里：容器内直连 `api.github.com` 被重置，而且失败被静默吞掉，最后以一个 `.../download/v/...` 的 404 冒出来（这个吞失败的 bug 已修，现在会当场报错并提示 pin 版本）。

`HTTP_PROXY` / `HTTPS_PROXY` 是 Docker 预定义 build-arg，透传宿主代理即可，不用改任何文件：

```sh
docker build -f deploy/panel/Dockerfile.core   --build-arg HTTP_PROXY=http://host.docker.internal:7897   --build-arg HTTPS_PROXY=http://host.docker.internal:7897   -t chiral-core .
```

（`host.docker.internal` 是容器视角的宿主机；把 7897 换成你的代理端口。agent 镜像同理。）

## 关键决策

- **Panel 镜像里也要有 Xray 二进制**：面板在存版本 / 下发前要用 `xray -test` 校验渲染出的 config，并派生 Go 标准库没有的 ML-DSA-65 密钥。geo 资源（`geoip.dat` / `geosite.dat`）必须一并装进镜像——少了它们，用到 `geosite:` / `geoip:` 的路由规则加载失败，`xray -test` 会拒掉在节点上完全合法的 config。
- 两个镜像的 Xray 都走 **snapshot（prerelease）通道**：xhttp 上下行分离、后量子密钥交换这类特性只在快照版有。默认构建时解析最新发布版；发布流程可显式传入 release tag 复现单次构建。
- Agent 用 `network_mode: host`：Xray 要在节点上绑各式 inbound 端口，否则每改一次 config 都得重新声明端口映射。
- Core 二进制 `CGO_ENABLED=0` 全静态（`modernc.org/sqlite` 是纯 Go），运行阶段落在 alpine 上、非 root 用户跑。
- `agent/docker-compose.yml.tmpl` 与 `core/internal/api` 的 `composeSnippet` 是两份**必须手工同步**的东西，改一边记得改另一边。
