# remote-workspace/ 远程工作站

远程终端（SSH PTY）与车端图形程序（RViz/RQt/Gazebo）的远程显示实现。
原则：受控远程工作站、临时授权、完整审计；不直接暴露 SSH。
具体实现和验收边界见 [`../docs/architecture-functional-specification.md`](../docs/architecture-functional-specification.md) 的远程工作空间章节。
