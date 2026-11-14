export interface RuntimeBootMeta {
  edition?: 'server' | 'desktop';
  authEnabled?: boolean;
  desktopToken?: string;
  darkMode?: boolean;
}

declare global {
  interface Window {
    __DBHARBOR_BOOT__?: RuntimeBootMeta;
  }
}

export function getRuntimeBootMeta(): RuntimeBootMeta {
  if (typeof window !== 'undefined' && window.__DBHARBOR_BOOT__) {
    return window.__DBHARBOR_BOOT__;
  }
  return { edition: 'server', authEnabled: true };
}

export function isDesktopRuntime(): boolean {
  const meta = getRuntimeBootMeta();
  return meta.edition === 'desktop' || meta.authEnabled === false;
}

export function resolveInitialDarkMode(): boolean {
  try {
    if (typeof localStorage !== 'undefined') {
      const saved = localStorage.getItem('dbharbor_theme');
      if (saved === 'dark') return true;
      if (saved === 'light') return false;
    }
  } catch {
    // Ignore storage access errors in restricted webviews
  }
  const meta = getRuntimeBootMeta();
  if (typeof meta.darkMode === 'boolean') {
    return meta.darkMode;
  }
  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    return window.matchMedia('(prefers-color-scheme: dark)').matches;
  }
  return false;
}
