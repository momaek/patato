# Patato

Agentic 时代的代理面板：人只负责说话和审批，日常操作交给 AI Agent。

- 与 [tolato](https://github.com/momaek/tolato) 搭配：tolato 管机器，Patato 管节点、套餐、用户和订阅。
- API 优先、意图级接口、写操作可预演、按风险分级审批。
- 节点侧兼容 Xboard / V2board 的 UniProxy 协议，V2bX / XrayR 无需改动即可接入。
- 二期提供自研节点端 patato-node：WebSocket 推送、带序号的流量上报、经 tolato-agent 免密钥接入。

设计文档：[docs/design.md](docs/design.md)

状态：设计阶段。
