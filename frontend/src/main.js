import { createApp } from 'vue'
import App from './App.vue'
import router from './router'
import './style.css'
import './layout.css'
// Imported last on purpose: it owns every breakpoint and overrides the base
// layout as the window narrows (see the header comment in responsive.css).
import './responsive.css'

const app = createApp(App)
app.use(router)
app.mount('#app')
