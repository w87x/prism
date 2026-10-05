import { mount } from 'svelte';
import '@fontsource/jetbrains-mono/400.css';
import '@fontsource/jetbrains-mono/500.css';
import '@fontsource/jetbrains-mono/700.css';
import './app.css';
import './themes.css';
import { applyTheme, currentTheme } from './themes.js';
import App from './App.svelte';
import { installTableSort } from './lib/tablesort.js';

applyTheme(currentTheme(), false); // before mount: no flash of the default colours
installTableSort();
mount(App, { target: document.getElementById('app') });
