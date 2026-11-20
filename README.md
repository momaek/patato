# Patato

Agentic 时代的代理面板：人只负责说话和审批，日常操作交给 AI Agent。

- 与 [tolato](https://github.com/momaek/tolato) 搭配：tolato 管机器，Patato 管节点、套餐、用户和订阅。
- API 优先、意图级接口、写操作可预演、按风险分级审批。
- 节点侧兼容 Xboard / V2board 的 UniProxy 协议，V2bX / XrayR 无需改动即可接入。
- 二期提供自研节点端 patato-node：WebSocket 推送、带序号的流量上报、经 tolato-agent 免密钥接入。

设计文档：[docs/design.md](docs/design.md)

界面设计稿：[docs/ui](docs/ui/README.md)

开发计划：[docs/plan.md](docs/plan.md)

中转线路与落地访问监控计划：[docs/monitoring.md](docs/monitoring.md)，中转详细设计：[docs/relay.md](docs/relay.md)

服务端说明：[docs/server.md](docs/server.md)

部署：两个前端内嵌在服务端镜像中，无需单独部署静态文件；Docker 构建使用 `server/Dockerfile`，本地完整构建使用 `make build`（需要 Go 和 pnpm）。开发时仍可用 Vite 热更新，见 [docs/web.md](docs/web.md)。

状态：MVP 与二期（支付与订单、工单、优惠券与邀请返佣、Admin 手动操作与报表、更多订阅格式、Webhook 与自动处置、多人审批）已完成，patato-node 可以经 tolato-agent 引导身份接入（需要 tolato 的配套改动；见 [docs/server.md](docs/server.md)、[docs/web.md](docs/web.md)、[docs/cli.md](docs/cli.md)）；已进行 [真机核心验收](docs/acceptance-2026-11-04.md)，核心功能可达，但稳定性未通过，尚不代表生产就绪；发布流程和迁移演练尚未完成。
