import React from 'react';
import { useResponsive } from '../../hooks/useResponsive';
import { MonacoEditorSurface } from './MonacoEditorSurface';

export interface MonacoCodeEditorProps {
  value: string;
  language: 'sql' | 'json';
  height?: number | string;
  darkMode?: boolean;
  readOnly?: boolean;
  onChange?: (val: string) => void;
}

export const MonacoCodeEditorInner: React.FC<MonacoCodeEditorProps> = ({
  value,
  language,
  darkMode,
  readOnly = false,
  onChange,
}) => {
  const { isMobile } = useResponsive();
  return (
    <MonacoEditorSurface
      value={value}
      language={language}
      darkMode={darkMode}
      readOnly={readOnly}
      compactGutter={isMobile}
      fontSize={isMobile ? 12 : 13}
      onChange={onChange}
    />
  );
};

export default MonacoCodeEditorInner;
