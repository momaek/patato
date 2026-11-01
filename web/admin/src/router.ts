import { createRouter, createWebHistory } from 'vue-router'
import { useSession } from './lib/session'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: () => import('./pages/Login.vue'), meta: { public: true } },
    // 运营者邀请链接：patato-server operator invite 输出 {admin_url}/setup?invite=...
    { path: '/setup', name: 'setup', component: () => import('./pages/Setup.vue'), meta: { public: true } },
    {
      path: '/',
      component: () => import('./components/Layout.vue'),
      children: [
        { path: '', name: 'overview', component: () => import('./pages/Overview.vue') },
        { path: 'approvals', name: 'approvals', component: () => import('./pages/Approvals.vue') },
        // Telegram 卡片和 CLI 都链接到这里；CLI 拉起时带 ?via=cli
        { path: 'approvals/:id', name: 'approval', component: () => import('./pages/Approval.vue') },
        { path: 'nodes', name: 'nodes', component: () => import('./pages/Nodes.vue') },
        { path: 'nodes/:id', name: 'node', component: () => import('./pages/Node.vue') },
        { path: 'machines', name: 'machines', component: () => import('./pages/Machines.vue') },
        { path: 'plans', name: 'plans', component: () => import('./pages/Plans.vue') },
        { path: 'rules', name: 'rules', component: () => import('./pages/RuleProfiles.vue') },
        { path: 'users', name: 'users', component: () => import('./pages/Users.vue') },
        { path: 'orders', name: 'orders', component: () => import('./pages/Orders.vue') },
        { path: 'tickets', name: 'tickets', component: () => import('./pages/Tickets.vue') },
        { path: 'tickets/:id', name: 'ticket', component: () => import('./pages/Ticket.vue') },
        { path: 'coupons', name: 'coupons', component: () => import('./pages/Coupons.vue') },
        { path: 'referrals', name: 'referrals', component: () => import('./pages/Referrals.vue') },
        { path: 'reports', name: 'reports', component: () => import('./pages/Reports.vue') },
        { path: 'integrations', name: 'integrations', component: () => import('./pages/Integrations.vue') },
        { path: 'audit', name: 'audit', component: () => import('./pages/Audit.vue') },
        { path: 'account', name: 'account', component: () => import('./pages/Account.vue') },
        // CLI 登录的设备授权页：服务端给 CLI 的 verification_url 是 {admin_url}/cli-auth?code=...
        { path: 'cli-auth', name: 'cli-auth', component: () => import('./pages/CliAuth.vue') },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  if (to.meta.public) return true
  const session = useSession()
  if (!session.checked) await session.load().catch(() => null)
  if (!session.me) return { name: 'login', query: { next: to.fullPath } }
  return true
})
