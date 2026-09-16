# workspace-agent/ 车端远程终端代理

远驾不只"开车"，还要能登录车端电脑：看日志、重启进程、跑诊断。
本代理是该场景的车端入口，负责受控 PTY、断线恢复和维护审计。

## 四道安全门（终端是最危险的接口，门禁比控制更严）

1. **先授权再连接**：只认控制权服务签发、经 Gateway UDS 送达的终端令牌
   （`platform.v1.TerminalGrant`）；无效/过期一律拒绝并记审计。Gateway 到 Agent
   的本机通道使用长度前缀 Protobuf，禁止 JSON-line。
2. **全审计**：attach / detach / denied / blocked 全部写
   `/tmp/ra-workspace-audit.log`（JSON 行）
3. **危险命令拦截**：黑名单（`rm -rf /`、`reboot`、`shutdown`、`mkfs` 等）
   不发给 shell，明确回复 `[BLOCKED]`
4. **会话保活**：断线不杀 shell（简化版 tmux）；重连回放最近 64KB 屏幕
   并接着用——对应验收标准"断线后终端可恢复"

## 运行契约（`terminal_agent.py`）

- 以 `workspace` 角色经 UDS 接入 Gateway 收令牌；仅接受绑定车辆/Gateway 的
  `TerminalGrant`；远端附着必须使用 TLS 1.3 + 客户端证书（独占单连接），
  且请求必须同时提供 `session_id` 和对应短期令牌。
- 握手：客户端发 `{"token": ...}`，校验通过后回放滚动缓冲再转实时流
- PTY 会话：`bash --norc --noprofile`，TERM=dumb

## 安全边界

- 工作空间令牌必须由受信 Gateway 下发并在车端校验有效期；未握手的本地
  组件不会获得令牌转发。
- 控制命令与工作空间通道相互隔离；终端代理不能接收或执行 ControlCommand。
- shell 输入按策略阻断并写审计；高风险操作应由正式审批/受限 shell 策略
  在执行前拒绝，不能把浏览器提示当作安全边界。
- 断开连接只释放客户端，不自动销毁已批准的会话；会话输出保留在受限滚动
  缓冲中，重连时回放并记录审计。

启动时必须显式提供车辆 ID、Gateway ID、监听端口、Gateway UDS、服务端证书、
私钥、客户端 CA、Envelope HMAC 密钥和 Authority 公钥；无参数启动会拒绝。未完成 Workspace Broker、审批策略和
受限 shell 部署前，不得把该 Agent 端口接入生产公网。
