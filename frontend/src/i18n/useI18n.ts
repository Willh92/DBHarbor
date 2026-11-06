import { useCallback } from 'react';
import { translateWithLocale, type AppLocale } from './index';
import { useWorkbenchStore } from '../store/useWorkbenchStore';

export function useI18n() {
  const locale = useWorkbenchStore((s) => s.locale);
  const setLocale = useWorkbenchStore((s) => s.setLocale);

  const t = useCallback(
    (enTemplate: string, vars?: Record<string, string | number>) =>
      translateWithLocale(locale, enTemplate, vars),
    [locale]
  );

  return { locale, setLocale, t } as {
    locale: AppLocale;
    setLocale: (loc: AppLocale) => void;
    t: (enTemplate: string, vars?: Record<string, string | number>) => string;
  };
}
