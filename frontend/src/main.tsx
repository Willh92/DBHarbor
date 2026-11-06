import React from 'react';
import ReactDOM from 'react-dom/client';
import './index.css';
import App from './App';
import { detectInitialLocale, ensureLocaleLoaded } from './i18n';

const rootEl = document.getElementById('root')!;

ensureLocaleLoaded(detectInitialLocale()).finally(() => {
  ReactDOM.createRoot(rootEl).render(
    <React.StrictMode>
      <App />
    </React.StrictMode>
  );
});
