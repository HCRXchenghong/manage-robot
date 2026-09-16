# protocols/ 平台协议（全端共用的“语言”）

整个仓库最重要的目录——所有端（车、云、客户端、Web）只认这里定义的消息。

| 子目录 | 内容 |
|---|---|
| `protobuf/` | 全部跨端消息：能力声明、遥测、控制、地图发布/ACK、网络状态、审计 |
| `vss/` | COVESA VSS 版本、平台 `Platform.*` overlay 与映射表 |
| `openapi/` | 管理 API 接口定义 |
| `compatibility/` | 兼容矩阵与 golden 测试向量 |
| `buf.yaml` | Buf 配置：schema lint 与破坏性变更检查 |

## 生成绑定

Protobuf 源文件只维护在 `protobuf/platform/v1/`；提交的绑定位于：

- `platform/v1/*.pb.go`：Go 服务与车端安全内核使用；
- `gen/python/robot_agent_platform/v1/*_pb2.py`：Python Gateway、中继和客户端使用。

生成前需要 `protoc`、`protoc-gen-go` 和 Python `protobuf` 运行时：

```bash
protoc -I protocols/protobuf \
  --go_out=paths=source_relative:protocols \
  protocols/protobuf/platform/v1/*.proto
protoc -I protocols/protobuf \
  --python_out=protocols/gen/python \
  protocols/protobuf/platform/v1/*.proto
```

Python 绑定使用 `robot_agent_platform` 作为导入包名，避免遮蔽 Python
标准库的 `platform` 模块；描述符中的协议包名仍固定为 `platform.v1`。

规则：先改这里的协议，再改各端代码；平台接口稳定，实现可替换。
