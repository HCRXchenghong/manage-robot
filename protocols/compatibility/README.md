# compatibility/ 兼容矩阵与 golden 向量

## 放什么

- **兼容矩阵**：哪种车端栈版本 + Adapter 版本 + 平台协议版本允许组合，
  哪些组合可以远驾、哪些只允许监控（对应架构文档 §6.4 服务端策略）。
- **golden 向量**：一批“标准答案”消息样本（字节级），
  任何端的编解码实现都必须和它完全一致，防止各端“各自理解”。

## 约束

兼容矩阵按车辆栈、Adapter、Gateway、Safety Arbiter 和平台协议版本维护；
golden 向量由受控协议测试夹具生成并入库。任何字段编号、单位、枚举或认证
语义变更都必须同步更新矩阵和字节级向量，未通过兼容性检查的组合只能拒绝接入。

当前机器可读矩阵的唯一事实文件是
`protocols/platform/v1/compatibility_matrix.json`，由协议 Go 包编译内嵌，
避免运行时依赖工作目录。矩阵条目采用精确版本匹配，至少声明车端栈、Gateway、
Adapter、Safety Arbiter、Topic 映射和控制模式；`requires_tpm_for_control` 与
`required_modems_for_control` 只决定是否具备控制资格，不会把缺少硬件的车辆
伪装成可接管车辆。Fleet 会保存每次能力声明的版本化摘要和准入结果，Gateway
声明的组织分组只作为证据，不改变平台侧资源归属。
