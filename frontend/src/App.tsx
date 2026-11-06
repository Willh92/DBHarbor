import React, { Suspense, lazy, useEffect, useState } from 'react';
import { ConfigProvider, Spin, theme as antdTheme } from 'antd';
import type { Locale } from 'antd/es/locale';
import enUS from 'antd/locale/en_US';
import { api } from './api/client';
import { useI18n } from './i18n/useI18n';
import { useWorkbenchStore } from './store/useWorkbenchStore';

const WorkbenchLayout = lazy(() =>
  import('./layouts/WorkbenchLayout').then((m) => ({ default: m.WorkbenchLayout }))
);

const LoginPage = lazy(() =>
  import('./pages/LoginPage').then((m) => ({ default: m.LoginPage }))
);

export const App: React.FC = () => {
  const { isDesktop, token, darkMode, logout } = useWorkbenchStore();
  const { locale, t } = useI18n();
  const [antdLocale, setAntdLocale] = useState<Locale>(enUS);

  useEffect(() => {
    if (isDesktop) return;
    const handler = () => logout(false);
    window.addEventListener('dbharbor:unauthorized', handler);
    return () => window.removeEventListener('dbharbor:unauthorized', handler);
  }, [isDesktop, logout]);

  useEffect(() => {
    document.documentElement.lang = locale;
    document.title = isDesktop
      ? `DBHarbor — ${t('Multi-Database Desktop Workbench')}`
      : `DBHarbor — ${t('Multi-Database Web Workbench')}`;
  }, [isDesktop, locale, t]);

  useEffect(() => {
    const themeStr = darkMode ? 'dark' : 'light';
    document.documentElement.dataset.theme = themeStr;
    document.documentElement.style.colorScheme = themeStr;
    let metaTheme = document.querySelector('meta[name="theme-color"]') as HTMLMetaElement | null;
    if (!metaTheme) {
      metaTheme = document.createElement('meta');
      metaTheme.name = 'theme-color';
      document.head.appendChild(metaTheme);
    }
    metaTheme.content = darkMode ? '#141414' : '#ffffff';
    if (isDesktop) {
      api.setDesktopWindowTheme(darkMode).catch(() => {});
    }
  }, [darkMode, isDesktop]);

  useEffect(() => {
    let active = true;
    if (locale === 'en-US') {
      setAntdLocale(enUS);
      return;
    }
    const loader =
      locale === 'zh-CN'
        ? () => import('antd/locale/zh_CN')
        : locale === 'zh-TW'
          ? () => import('antd/locale/zh_TW')
          : () => import('antd/locale/ja_JP');
    loader()
      .then((m) => {
        if (active) setAntdLocale(m.default);
      })
      .catch(() => {
        if (active) setAntdLocale(enUS);
      });
    return () => {
      active = false;
    };
  }, [locale]);

  const loadingFallback = (
    <div style={{ height: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
      <Spin size="large" tip={t('Loading DBHarbor Workbench...')} />
    </div>
  );

  return (
    <ConfigProvider
      locale={antdLocale}
      theme={{
        algorithm: darkMode ? antdTheme.darkAlgorithm : antdTheme.defaultAlgorithm,
        token: {
          colorPrimary: '#1677ff',
          borderRadius: 6,
        },
      }}
    >
      <div
        style={{
          minHeight: '100dvh',
          background: darkMode ? '#0d1017' : '#ffffff',
          color: darkMode ? '#e6edf3' : '#1f2328',
        }}
      >
        <Suspense fallback={loadingFallback}>
          {isDesktop || token ? <WorkbenchLayout /> : <LoginPage />}
        </Suspense>
      </div>
    </ConfigProvider>
  );
};

export default App;
