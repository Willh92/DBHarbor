import { LOCALE_OPTIONS, type AppLocale } from './locales/meta';

export { LOCALE_OPTIONS, type AppLocale };

const LOCALE_STORAGE_KEY = 'dbharbor_locale';

const loadedDicts: Partial<Record<AppLocale, Record<string, string>>> = {};
const inflightLoads: Partial<Record<AppLocale, Promise<void>>> = {};

export function detectInitialLocale(): AppLocale {
  if (typeof window === 'undefined') return 'en-US';
  const saved = localStorage.getItem(LOCALE_STORAGE_KEY) as AppLocale | null;
  if (saved && LOCALE_OPTIONS.some((o) => o.value === saved)) {
    return saved;
  }
  const nav = (navigator.language || '').toLowerCase();
  if (nav.startsWith('zh-tw') || nav.startsWith('zh-hk') || nav.startsWith('zh-mo')) {
    return 'zh-TW';
  }
  if (nav.startsWith('zh')) {
    return 'zh-CN';
  }
  if (nav.startsWith('ja')) {
    return 'ja-JP';
  }
  return 'en-US';
}

export async function ensureLocaleLoaded(locale: AppLocale): Promise<void> {
  if (locale === 'en-US' || loadedDicts[locale]) {
    return;
  }
  if (inflightLoads[locale]) {
    return inflightLoads[locale];
  }
  const p = (async () => {
    try {
      if (locale === 'zh-CN') {
        const m = await import('./locales/zhCN');
        loadedDicts['zh-CN'] = m.ZH_CN_DICT;
      } else if (locale === 'zh-TW') {
        const m = await import('./locales/zhTW');
        loadedDicts['zh-TW'] = m.ZH_TW_DICT;
      } else if (locale === 'ja-JP') {
        const m = await import('./locales/jaJP');
        loadedDicts['ja-JP'] = m.JA_JP_DICT;
      }
    } finally {
      delete inflightLoads[locale];
    }
  })();
  inflightLoads[locale] = p;
  return p;
}

export function persistLocale(locale: AppLocale) {
  if (typeof window !== 'undefined') {
    localStorage.setItem(LOCALE_STORAGE_KEY, locale);
    document.documentElement.lang = locale;
  }
}

export function translateWithLocale(
  locale: AppLocale,
  enTemplate: string,
  vars?: Record<string, string | number>
): string {
  const dict = loadedDicts[locale];
  let tpl = dict ? dict[enTemplate] || enTemplate : enTemplate;

  if (vars) {
    Object.entries(vars).forEach(([k, v]) => {
      tpl = tpl.replace(new RegExp(`\\{${k}\\}`, 'g'), String(v));
    });
  }
  return tpl;
}
