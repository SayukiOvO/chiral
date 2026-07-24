# secret — 静态加密

SQLite 里三类东西加密存储：

1. **变量池的私钥分量**（REALITY `privateKey`、后量子 seed）
2. **渲染后的节点 config**（`node_configs.config`）——它按设计就含私钥明文，**不加密的话上面第 1 条等于白做**：拿到库文件的人直接 `select config from node_configs` 就能读到每个节点的密钥
3. **节点 config 骨架**（`nodes.config_skeleton`）——可能含上游代理凭证等

- **算法**：AES-256-GCM，密钥 = `SHA-256(CHIRAL_SECRET_KEY)`。
- **AAD 绑定**：密文绑定到具体的行（变量 `变量ID:分量名`、config `node-config:节点ID:版本号`、骨架 `node-skeleton:节点ID`），所以有数据库写权限的人**无法把密文挪到另一行**上——包括把旧版本 config 冒充成新版本。
- **格式自描述**：`enc:v1:<keyID>:<base64>` / `plain:<值>`。`keyID` 是密钥的短指纹——换了 `CHIRAL_SECRET_KEY` 会得到一句明确的「这是用另一把密钥加的」，而不是一个难懂的认证失败。
- **可后开**：没配密钥时明文存（启动告警）；之后配上密钥，旧的明文值仍能正常读出。

## 威胁模型

防的是**数据库文件外流**（备份被拿走、磁盘快照）——光有 SQLite 文件没有用。

**不防**已经能在面板机上执行代码的攻击者：他们能读环境变量。

## 待办

- 密钥轮换：换 `CHIRAL_SECRET_KEY` 后批量解密重封的流程。
