import './styles/global.css';
import { mount } from 'svelte';
import App from './App.svelte';

const target = document.getElementById('app');
if (!target) {
  throw new Error('Mount target #app not found');
}

// Svelte 5 components are mounted with the runtime helper. The legacy
// `new App({ target })` API leaves the packaged Wails webview blank.
const app = mount(App, { target });

export default app;
