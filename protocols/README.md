# protocols/ 平台协议（全端共用的“语言”）

整个仓库最重要的目录——所有端（车、云、客户端、Web）只认这里定义的消息。

| 子目录 | 内容 |
|---|---|
| `protobuf/` | 全部跨端消息：能力声明、遥测、控制、ACK、网络状态、审计 |
| `vss/` | COVESA VSS 版本、平台 `Platform.*` overlay 与映射表 |
| `openapi/` | 管理 API 接口定义 |
| `compatibility/` | 兼容矩阵与 golden 测试向量 |
| `buf.yaml` | Buf 配置：schema lint 与破坏性变更检查 |

规则：先改这里的协议，再改各端代码；平台接口稳定，实现可替换。
