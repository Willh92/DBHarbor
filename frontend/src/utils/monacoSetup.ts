// Tree-shaken Monaco setup: imports only core SQL/JSON editor capabilities + 1 lightweight worker,
// eliminating 80+ unused language grammars, TypeScript/HTML/CSS language services, and json.worker.
import 'monaco-editor/esm/vs/editor/browser/coreCommands.js';
import 'monaco-editor/esm/vs/editor/browser/widget/codeEditor/codeEditorWidget.js';
import 'monaco-editor/esm/vs/editor/contrib/bracketMatching/browser/bracketMatching.js';
import 'monaco-editor/esm/vs/editor/contrib/clipboard/browser/clipboard.js';
import 'monaco-editor/esm/vs/editor/contrib/comment/browser/comment.js';
import 'monaco-editor/esm/vs/editor/contrib/contextmenu/browser/contextmenu.js';
import 'monaco-editor/esm/vs/editor/contrib/cursorUndo/browser/cursorUndo.js';
import 'monaco-editor/esm/vs/editor/contrib/find/browser/findController.js';
import 'monaco-editor/esm/vs/editor/contrib/folding/browser/folding.js';
import 'monaco-editor/esm/vs/editor/contrib/indentation/browser/indentation.js';
import 'monaco-editor/esm/vs/editor/contrib/lineSelection/browser/lineSelection.js';
import 'monaco-editor/esm/vs/editor/contrib/linesOperations/browser/linesOperations.js';
import 'monaco-editor/esm/vs/editor/contrib/multicursor/browser/multicursor.js';
import 'monaco-editor/esm/vs/editor/contrib/suggest/browser/suggestController.js';
import 'monaco-editor/esm/vs/editor/contrib/tokenization/browser/tokenization.js';
import 'monaco-editor/esm/vs/editor/contrib/wordHighlighter/browser/wordHighlighter.js';
import 'monaco-editor/esm/vs/editor/contrib/wordOperations/browser/wordOperations.js';
import 'monaco-editor/esm/vs/base/browser/ui/codicons/codiconStyles.js';

import 'monaco-editor/esm/vs/basic-languages/sql/sql.contribution.js';

import * as monaco from 'monaco-editor/esm/vs/editor/editor.api.js';
import editorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';

// Lightweight Monarch JSON syntax highlighter & bracket config (avoids 356KB json.worker)
if (!monaco.languages.getLanguages().some((l) => l.id === 'json')) {
  monaco.languages.register({ id: 'json', extensions: ['.json', '.jsonl'] });
  monaco.languages.setLanguageConfiguration('json', {
    brackets: [
      ['{', '}'],
      ['[', ']'],
    ],
    autoClosingPairs: [
      { open: '{', close: '}', notIn: ['string'] },
      { open: '[', close: ']', notIn: ['string'] },
      { open: '"', close: '"', notIn: ['string'] },
    ],
  });
  monaco.languages.setMonarchTokensProvider('json', {
    tokenizer: {
      root: [
        [/"(?:[^"\\]|\\.)*"(?=\s*:)/, 'type.identifier'],
        [/"(?:[^"\\]|\\.)*"/, 'string'],
        [/-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/, 'number'],
        [/\b(?:true|false|null)\b/, 'keyword'],
        [/[{}[\],:]/, 'delimiter'],
      ],
    },
  });
}

const SQL_KEYWORDS = [
  'SELECT',
  'FROM',
  'WHERE',
  'ORDER BY',
  'GROUP BY',
  'HAVING',
  'LIMIT',
  'OFFSET',
  'JOIN',
  'LEFT JOIN',
  'INNER JOIN',
  'RIGHT JOIN',
  'INSERT INTO',
  'VALUES',
  'UPDATE',
  'SET',
  'DELETE FROM',
  'ALTER TABLE',
  'ADD COLUMN',
  'CREATE TABLE',
  'DROP TABLE',
  'TRUNCATE TABLE',
  'DISTINCT',
  'EXPLAIN',
  'WITH',
  'COUNT(*)',
];

interface IndexedSchemaEntry {
  label: string;
  lower: string;
  kind: monaco.languages.CompletionItemKind;
  detail: string;
}

const sqlModelAutocompleteMap = new Map<string, IndexedSchemaEntry[]>();
let sqlProviderRegistered = false;

function ensureSQLCompletionProvider() {
  if (sqlProviderRegistered) return;
  sqlProviderRegistered = true;

  monaco.languages.registerCompletionItemProvider('sql', {
    provideCompletionItems: (model, position) => {
      const entries = sqlModelAutocompleteMap.get(model.uri.toString());
      if (!entries) {
        return { suggestions: [] };
      }

      const word = model.getWordUntilPosition(position);
      const prefix = (word.word || '').toLowerCase();
      const range = {
        startLineNumber: position.lineNumber,
        endLineNumber: position.lineNumber,
        startColumn: word.startColumn,
        endColumn: word.endColumn,
      };

      const suggestions: monaco.languages.CompletionItem[] = [];
      for (const kw of SQL_KEYWORDS) {
        if (!prefix || kw.toLowerCase().startsWith(prefix)) {
          suggestions.push({
            label: kw,
            kind: monaco.languages.CompletionItemKind.Keyword,
            insertText: kw,
            range,
          });
        }
      }

      const maxItems = 400;
      for (let i = 0; i < entries.length && suggestions.length < maxItems; i++) {
        const item = entries[i];
        if (!prefix || item.lower.includes(prefix)) {
          suggestions.push({
            label: item.label,
            kind: item.kind,
            detail: item.detail,
            insertText: item.label,
            range,
          });
        }
      }

      return { suggestions };
    },
  });
}

export function setSQLModelAutocomplete(
  modelUri: string,
  tables: Record<string, string[]> = {},
  databases: string[] = []
) {
  if (!modelUri) return;
  ensureSQLCompletionProvider();

  const indexed: IndexedSchemaEntry[] = [];
  for (const db of databases) {
    if (db) {
      indexed.push({
        label: db,
        lower: db.toLowerCase(),
        kind: monaco.languages.CompletionItemKind.Module,
        detail: 'Database',
      });
    }
  }

  for (const [tbl, cols] of Object.entries(tables)) {
    indexed.push({
      label: tbl,
      lower: tbl.toLowerCase(),
      kind: monaco.languages.CompletionItemKind.Class,
      detail: 'Table',
    });
    for (const col of cols || []) {
      indexed.push({
        label: col,
        lower: col.toLowerCase(),
        kind: monaco.languages.CompletionItemKind.Field,
        detail: `${tbl}.${col}`,
      });
    }
  }

  sqlModelAutocompleteMap.set(modelUri, indexed);
}

export function clearSQLModelAutocomplete(modelUri: string) {
  if (!modelUri) return;
  sqlModelAutocompleteMap.delete(modelUri);
}

(self as any).MonacoEnvironment = {
  getWorker() {
    return new editorWorker();
  },
};

export { monaco };
