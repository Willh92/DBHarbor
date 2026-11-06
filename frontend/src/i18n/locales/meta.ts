export type AppLocale = 'en-US' | 'zh-CN' | 'zh-TW' | 'ja-JP';

export const LOCALE_OPTIONS: { value: AppLocale; label: string; short: string }[] = [
  { value: 'en-US', label: 'English (US)', short: 'EN' },
  { value: 'zh-CN', label: '简体中文 (Simplified Chinese)', short: '简中' },
  { value: 'zh-TW', label: '繁體中文 (Traditional Chinese)', short: '繁中' },
  { value: 'ja-JP', label: '日本語 (Japanese)', short: '日本語' },
];
