# pki/dev/ 开发证书（仅本地演示，严禁用于生产）

| 文件 | 用途 |
|---|---|
| `ca.crt/ca.key` | 开发 CA。生产环境用 Vault/企业 CA 替代 |
| `server.crt/key` | Broker 服务端证书（CN=localhost） |
| `vehicle.crt/key` | 车辆客户端证书（**CN=车辆ID**，mTLS 身份来源） |

生产路径（架构文档 §5.1）：密钥进 TPM 2.0/安全芯片，文件密钥仅开发环境；
证书签发与轮转由 `deploy/pki/` 的正式流程负责。

重新生成（开发用）：见第 5b 步提交记录中的 openssl 命令。
