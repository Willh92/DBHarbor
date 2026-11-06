import React, { Suspense, lazy } from 'react';
import { Spin } from 'antd';
import type { MonacoCodeEditorProps } from './MonacoCodeEditorInner';

const MonacoCodeEditorInner = lazy(() => import('./MonacoCodeEditorInner'));

export const LazyCodeEditor: React.FC<MonacoCodeEditorProps> = (props) => {
  return (
    <Suspense
      fallback={
        <div style={{ height: '100%', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          <Spin size="small" />
        </div>
      }
    >
      <MonacoCodeEditorInner {...props} />
    </Suspense>
  );
};
