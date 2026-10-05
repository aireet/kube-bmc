import { createRouter, createWebHistory } from 'vue-router'
import Fleet from './views/Fleet.vue'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'fleet', component: Fleet },
    { path: '/servers/:name', name: 'server', component: () => import('./views/Server.vue'), props: true },
    { path: '/actions', name: 'actions', component: () => import('./views/Actions.vue') },
    { path: '/login', name: 'login', component: () => import('./views/Login.vue') },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
  scrollBehavior: () => ({ top: 0 }),
})
