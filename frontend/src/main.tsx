import React from 'react';
import ReactDOM from 'react-dom/client';
import './index.css';
import App from './App';
import { detectInitialLocale, ensureLocaleLoaded } from './i18n';
import { resolveInitialDarkMode } from './utils/runtimeMode';

if (typeof document !== 'undefined') {
  const initialTheme = resolveInitialDarkMode() ? 'dark' : 'light';
  document.documentElement.dataset.theme = initialTheme;
  document.documentElement.style.colorScheme = initialTheme;
}

if (typeof window !== 'undefined' && 'ongesturestart' in window) {
  const preventSafariGestureZoom = (event: Event) => {
    event.preventDefault();
  };
  document.addEventListener('gesturestart', preventSafariGestureZoom, { passive: false });
  document.addEventListener('gesturechange', preventSafariGestureZoom, { passive: false });
  document.addEventListener('gestureend', preventSafariGestureZoom, { passive: false });
}

const rootEl = document.getElementById('root')!;

ensureLocaleLoaded(detectInitialLocale()).finally(() => {
  ReactDOM.createRoot(rootEl).render(
    <React.StrictMode>
      <App />
    </React.StrictMode>
  );
});
