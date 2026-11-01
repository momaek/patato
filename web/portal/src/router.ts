import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'home', component: () => import('./pages/Home.vue') },
    { path: '/login', name: 'login', component: () => import('./pages/Login.vue') },
    { path: '/forgot-password', name: 'forgot', component: () => import('./pages/Forgot.vue') },
    // 找回密码邮件里的链接，服务端拼的是 {portal_url}/reset-password?token=...
    { path: '/reset-password', name: 'reset-password', component: () => import('./pages/NewPassword.vue') },
    { path: '/password', name: 'password', component: () => import('./pages/ChangePassword.vue') },
    { path: '/reset-link', name: 'reset-link', component: () => import('./pages/ResetLink.vue') },
    // 邀请链接：{portal_url}/register?invite=...
    { path: '/register', name: 'register', component: () => import('./pages/Register.vue') },
    { path: '/buy', name: 'buy', component: () => import('./pages/Buy.vue') },
    { path: '/orders', name: 'orders', component: () => import('./pages/Orders.vue') },
    // 支付完成后支付平台跳回 {portal_url}/orders/{订单号}，服务端拼的地址
    { path: '/orders/:no', name: 'order', component: () => import('./pages/Order.vue') },
    { path: '/tickets', name: 'tickets', component: () => import('./pages/Tickets.vue') },
    // 工单回复邮件里的链接 {portal_url}/tickets/{id}
    { path: '/tickets/:id', name: 'ticket', component: () => import('./pages/Ticket.vue') },
    { path: '/invite', name: 'invite', component: () => import('./pages/Invite.vue') },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
  scrollBehavior: () => ({ top: 0 }),
})
