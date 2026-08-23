# control-authority/ 控制权服务（接管审批 + 租约发放）

架构文档 §9：驾驶员想动车，必须先来这里"申请接管"。批准后拿到
【租约 + fencing token】，带着它们发控制命令，车端仲裁器才认。
这是"没有有效授权，谁也动不了车"的发证机关。

## 核心概念

- **租约（lease）**：一张有期限的控制许可证。到期自动作废，命令转为
  `REJECTED_LEASE（租约已过期）`。阶段 0 用墙钟 `valid_until_unix_ns` 表达。
- **fencing token**：接管纪元号，全局单调递增、由本服务独家发放。
  新接管的 token 一定更大；旧客户端即使重放旧租约，也会因
  `token 太小` 被挡下。一个租约用同一个 token，客户端不自己改。
- **单人独占**：同一时刻只允许一个接管者；新申请自动顶掉旧租约。

## 当前实现（阶段 0 本地版，`authority_service.py`）

- UDP JSON 服务（默认 :9300）。操作：
  - `{"op":"request","driver":"alice"}` → 发新租约，并把 `LeaseGrant` 信封
    经 Gateway 推给车端
  - `{"op":"release","driver":"alice"}` → 撤销（推一张立即失效的空租约）
- 车端 `Arbiter.grant_lease()` 收到后更新有效租约与 `last_fencing`。

## 端到端演示（接管生命周期）

    # 终端 1/2/3：网关 + 车端 + 权限服务
    python3 vehicle/gateway/gateway.py
    python3 client/simulator/vehicle_side.py
    python3 server/control-authority/authority_service.py --lease-seconds 5
    # 终端 4：接管生命周期演示（A 申请 -> B 顶替 -> A 被拒 -> 到期停车）
    python3 client/simulator/takeover_demo.py

预期：A 先 ✓ 行驶；B 顶替后 A 的命令 ✗ 租约无效；B 行驶至租约到期后
也被 ✗ 拒绝，车端看门狗触发，自主减速并安全停稳。

## 后续（生产化）

- 与 `control-authority` 审批流、驾驶员身份/资质校验联动
- 租约签发走签名（防伪造），时间基准用授时同步
- 续租（renew）、优雅交还、接管状态机与审计落库

