import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';

const DIFF_STUB_ID = '\0virtual:monaco-diff-stub';
const MULTI_DIFF_STUB_ID = '\0virtual:monaco-multi-diff-stub';

function monacoDiffStubPlugin(): Plugin {
  return {
    name: 'monaco-diff-stub',
    enforce: 'pre',
    resolveId(source) {
      const norm = source.replace(/\\/g, '/');
      if (norm.endsWith('/widget/diffEditor/diffEditorWidget.js') || norm.endsWith('/diffEditorWidget.js')) {
        return DIFF_STUB_ID;
      }
      if (
        norm.endsWith('/widget/multiDiffEditor/multiDiffEditorWidget.js') ||
        norm.endsWith('/multiDiffEditorWidget.js')
      ) {
        return MULTI_DIFF_STUB_ID;
      }
      return null;
    },
    load(id) {
      if (id === DIFF_STUB_ID) {
        return 'export class DiffEditorWidget { dispose() {} }';
      }
      if (id === MULTI_DIFF_STUB_ID) {
        return 'export class MultiDiffEditorWidget { dispose() {} }';
      }
      return null;
    },
  };
}

export default defineConfig({
  plugins: [monacoDiffStubPlugin(), react()],
  esbuild: {
    legalComments: 'none',
    drop: ['debugger'],
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8787',
        changeOrigin: true,
      },
    },
  },
  build: {
    target: 'es2020',
    outDir: 'dist',
    emptyOutDir: true,
    cssCodeSplit: true,
    modulePreload: {
      polyfill: false,
    },
    chunkSizeWarningLimit: 3200,
    rollupOptions: {
      output: {
        manualChunks(id) {
          const norm = id.replace(/\\/g, '/');
          if (norm.includes('preload-helper') || norm.includes('commonjsHelpers')) {
            return 'vendor-react';
          }
          if (norm.includes('node_modules/monaco-editor') || norm.includes('node_modules/@monaco-editor')) {
            return 'vendor-monaco';
          }
          if (norm.includes('@ant-design/icons-svg')) {
            return 'vendor-icons';
          }
          if (norm.includes('node_modules/react') || norm.includes('node_modules/scheduler') || norm.includes('node_modules/zustand')) {
            return 'vendor-react';
          }
        },
      },
    },
  },
});
