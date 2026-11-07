import React, { useEffect, useRef } from 'react';
import { useWorkbenchStore } from '../../store/useWorkbenchStore';
import { clearSQLModelAutocomplete, monaco } from '../../utils/monacoSetup';

let modelInstanceSeq = 0;

export interface MonacoEditorSurfaceProps {
  value: string;
  language: 'sql' | 'json';
  darkMode?: boolean;
  readOnly?: boolean;
  compactGutter?: boolean;
  fontSize?: number;
  wordWrap?: 'on' | 'off';
  paddingTop?: number;
  paddingBottom?: number;
  onChange?: (val: string) => void;
  onMount?: (editor: monaco.editor.IStandaloneCodeEditor, monacoApi: typeof monaco) => void;
}

export const MonacoEditorSurface: React.FC<MonacoEditorSurfaceProps> = ({
  value,
  language,
  darkMode,
  readOnly = false,
  compactGutter = false,
  fontSize = 13,
  wordWrap = 'on',
  paddingTop = 8,
  paddingBottom = 8,
  onChange,
  onMount,
}) => {
  const storeDarkMode = useWorkbenchStore((s) => s.darkMode);
  const effectiveDarkMode = darkMode ?? storeDarkMode;
  const containerRef = useRef<HTMLDivElement | null>(null);
  const editorRef = useRef<monaco.editor.IStandaloneCodeEditor | null>(null);
  const modelRef = useRef<monaco.editor.ITextModel | null>(null);
  const suppressChangeRef = useRef(false);
  const onChangeRef = useRef(onChange);
  const onMountRef = useRef(onMount);

  onChangeRef.current = onChange;
  onMountRef.current = onMount;

  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;

    const ext = language === 'json' ? 'json' : 'sql';
    const uri = monaco.Uri.parse(`inmemory://dbharbor/editor-${++modelInstanceSeq}.${ext}`);
    const model = monaco.editor.createModel(value ?? '', language, uri);
    modelRef.current = model;

    monaco.editor.setTheme(effectiveDarkMode ? 'vs-dark' : 'vs');

    const editor = monaco.editor.create(el, {
      model,
      theme: effectiveDarkMode ? 'vs-dark' : 'vs',
      readOnly,
      minimap: { enabled: false },
      fontSize,
      fontFamily: "'JetBrains Mono', 'Cascadia Code', 'SF Mono', Consolas, Menlo, monospace",
      automaticLayout: false,
      scrollBeyondLastLine: false,
      wordWrap,
      tabSize: 2,
      lineNumbers: compactGutter ? 'off' : 'on',
      glyphMargin: false,
      folding: !compactGutter,
      contextmenu: !compactGutter,
      suggest: { showStatusBar: !compactGutter },
      scrollbar: { alwaysConsumeMouseWheel: false },
      lineDecorationsWidth: compactGutter ? 4 : 8,
      lineNumbersMinChars: compactGutter ? 0 : 3,
      padding: { top: paddingTop, bottom: paddingBottom },
    });
    editorRef.current = editor;

    const contentSub = editor.onDidChangeModelContent(() => {
      if (suppressChangeRef.current) return;
      onChangeRef.current?.(editor.getValue());
    });

    let rafId = 0;
    const ro = new ResizeObserver(() => {
      if (rafId) cancelAnimationFrame(rafId);
      rafId = requestAnimationFrame(() => {
        rafId = 0;
        editor.layout();
      });
    });
    ro.observe(el);

    onMountRef.current?.(editor, monaco);

    return () => {
      if (rafId) cancelAnimationFrame(rafId);
      ro.disconnect();
      contentSub.dispose();
      const uriStr = model.uri.toString();
      clearSQLModelAutocomplete(uriStr);
      editor.dispose();
      model.dispose();
      editorRef.current = null;
      modelRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const model = modelRef.current;
    if (!model) return;
    const nextVal = value ?? '';
    if (model.getValue() !== nextVal) {
      suppressChangeRef.current = true;
      model.setValue(nextVal);
      suppressChangeRef.current = false;
    }
  }, [value]);

  useEffect(() => {
    const model = modelRef.current;
    if (!model) return;
    if (model.getLanguageId() !== language) {
      monaco.editor.setModelLanguage(model, language);
    }
  }, [language]);

  useEffect(() => {
    monaco.editor.setTheme(effectiveDarkMode ? 'vs-dark' : 'vs');
  }, [effectiveDarkMode]);

  useEffect(() => {
    const ed = editorRef.current;
    if (!ed) return;
    ed.updateOptions({
      readOnly,
      fontSize,
      wordWrap,
      lineNumbers: compactGutter ? 'off' : 'on',
      folding: !compactGutter,
      contextmenu: !compactGutter,
      suggest: { showStatusBar: !compactGutter },
      scrollbar: { alwaysConsumeMouseWheel: false },
      lineDecorationsWidth: compactGutter ? 4 : 8,
      lineNumbersMinChars: compactGutter ? 0 : 3,
      padding: { top: paddingTop, bottom: paddingBottom },
    });
  }, [readOnly, fontSize, wordWrap, compactGutter, paddingTop, paddingBottom]);

  return <div ref={containerRef} style={{ width: '100%', height: '100%' }} />;
};

export default MonacoEditorSurface;
