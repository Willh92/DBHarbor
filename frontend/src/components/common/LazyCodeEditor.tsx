import React, { Suspense, lazy } from 'react';
import { Spin } from 'antd';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import type { MonacoCodeEditorProps } from './MonacoCodeEditorInner';

const MonacoCodeEditorInner = lazy(() => import('./MonacoCodeEditorInner'));

export const LazyCodeEditor: React.FC<MonacoCodeEditorProps> = ({ height, darkMode, ...rest }) => {
  const storeDarkMode = useWorkbenchStore((s) => s.darkMode);
  const resolvedDark = darkMode ?? storeDarkMode;
  const inner = (
    <Suspense
      fallback={
        <div style={{ height: height || '100%', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <Spin size="small" />
        </div>
      }
    >
      <MonacoCodeEditorInner {...rest} darkMode={resolvedDark} />
    </Suspense>
  );
  if (height !== undefined) {
    return <div style={{ height, width: '100%', overflow: 'hidden' }}>{inner}</div>;
  }
  return inner;
};
