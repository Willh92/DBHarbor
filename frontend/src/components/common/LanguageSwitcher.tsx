import React from 'react';
import { Button, Dropdown, Space } from 'antd';
import { GlobalOutlined } from '@ant-design/icons';
import { LOCALE_OPTIONS, type AppLocale } from '../../i18n';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';

interface Props {
  size?: 'small' | 'middle' | 'large';
  shape?: 'default' | 'circle' | 'round';
  showLabel?: boolean;
}

export const LanguageSwitcher: React.FC<Props> = ({ size = 'small', shape = 'default', showLabel = true }) => {
  const { locale, setLocale } = useWorkbenchStore();
  const current = LOCALE_OPTIONS.find((o) => o.value === locale) || LOCALE_OPTIONS[0];

  return (
    <Dropdown
      trigger={['click']}
      placement="bottomRight"
      menu={{
        selectedKeys: [locale],
        items: LOCALE_OPTIONS.map((opt) => ({
          key: opt.value,
          label: opt.label,
          onClick: () => setLocale(opt.value as AppLocale),
        })),
      }}
    >
      <Button size={size} shape={shape} icon={<GlobalOutlined />}>
        {showLabel && shape !== 'circle' ? <Space size={4}>{current.short}</Space> : null}
      </Button>
    </Dropdown>
  );
};
