# workspace-agent/ 车端远程终端代理

远驾不只"开车"，还要能登录车端电脑：看日志、重启进程、跑诊断。
本代理是该场景的车端入口（架构文档阶段 2："SSH PTY、断线后终端可恢复"）。

## 四道安全门（终端是最危险的接口，门禁比控制更严）

1. **先授权再连接**：只认控制权服务签发、经 Gateway UDS 送达的终端令牌
   （`TerminalGrant`）；无效/过期一律拒绝并记审计
2. **全审计**：attach / detach / denied / blocked 全部写
   `/tmp/ra-workspace-audit.log`（JSON 行）
3. **危险命令拦截**：黑名单（`rm -rf /`、`reboot`、`shutdown`、`mkfs` 等）
   不发给 shell，明确回复 `[BLOCKED]`
4. **会话保活**：断线不杀 shell（简化版 tmux）；重连回放最近 64KB 屏幕
   并接着用——对应验收标准"断线后终端可恢复"

## 当前实现（阶段 0 本地版，`terminal_agent.py`）

- 经 UDS 接入 Gateway 收令牌；TCP :9600 等待远端附着（独占单连接）
- 握手：客户端发 `{"token": ...}`，校验通过后回放滚动缓冲再转实时流
- PTY 会话：`bash --norc --noprofile`，TERM=dumb

## 端到端演示

    # 终端 1/2/3：网关 + 权限服务 + 终端代理
    python3 vehicle/gateway/gateway.py
    python3 server/control-authority/authority_service.py
    python3 vehicle/workspace-agent/terminal_agent.py
    # 终端 4：自动验收（四道安全门 + 断线重连，6 项断言）
    python3 client/core/workspace_demo.py
    # 或真人交互：
    python3 client/core/terminal_client.py --interactive

## 已知限制（阶段 0）

- 黑名单按输入块扫描，逐字符敲击可绕过；生产应在 shell 层
  （受限 shell / 命令白名单 / 审批流）做纵深
- 单会话独占；生产支持多会话、角色分权、双人复核高危命令

## 后续（生产化）

- 走正式加密通道（QUIC 流 / SSH），令牌改短期签名凭证
- 审计上云落库、高危命令审批、命令录像回放

