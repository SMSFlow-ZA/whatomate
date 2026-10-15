import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { VueQueryPlugin } from '@tanstack/vue-query'

import App from './App.vue'
import router from './router'
import { i18n } from './i18n'

import './assets/fonts.css'
import './assets/index.css'
import { useBrandingStore } from './stores/branding'

const app = createApp(App)

app.use(createPinia())
app.use(router)
app.use(VueQueryPlugin)
app.use(i18n)

useBrandingStore().load().finally(() => app.mount('#app'))
