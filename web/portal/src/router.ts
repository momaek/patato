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
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
  scrollBehavior: () => ({ top: 0 }),
})
