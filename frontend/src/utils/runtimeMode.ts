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
