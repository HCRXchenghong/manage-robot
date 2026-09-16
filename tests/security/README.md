# 安全攻击回归测试

车端安全内核的可重复攻击测试位于：

```bash
cd Robot-agent/vehicle/safety-arbiter
go test ./...
```

覆盖的高风险场景：伪造 Authority 租约、重复控制帧、旧 fencing、TTL 过期、
控制内容篡改（MAC 失效）和本地控制流看门狗超时。任何一个用例失败都不得将
对应版本部署到真实车辆。

HIL/封闭场地测试范围还包括证书吊销、Broker ACL、Authority 主备切换、双链路
乱序/丢包和实际底盘最小风险动作证据；自动化测试结果不能替代这些安全证据。
