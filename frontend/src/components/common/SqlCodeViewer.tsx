import React, { useMemo } from 'react';

interface SqlCodeViewerProps {
  code: string;
  darkMode?: boolean;
  height?: number | string;
}

const SQL_KEYWORD_SET = new Set([
  'CREATE', 'TABLE', 'VIEW', 'INDEX', 'UNIQUE', 'PRIMARY', 'FOREIGN', 'KEY', 'REFERENCES',
  'ALTER', 'ADD', 'DROP', 'MODIFY', 'COLUMN', 'CONSTRAINT', 'ON', 'DELETE', 'UPDATE',
  'CASCADE', 'RESTRICT', 'SET', 'NULL', 'NOT', 'DEFAULT', 'AUTO_INCREMENT', 'AUTOINCREMENT',
  'GENERATED', 'ALWAYS', 'AS', 'IDENTITY', 'IF', 'EXISTS', 'SELECT', 'FROM', 'WHERE',
  'GROUP', 'ORDER', 'BY', 'HAVING', 'LIMIT', 'OFFSET', 'JOIN', 'LEFT', 'RIGHT', 'INNER',
  'ENGINE', 'CHARSET', 'CHARACTER', 'COLLATE', 'COMMENT', 'PARTITION', 'WITH', 'WITHOUT',
  'ROWID', 'STRICT', 'CHECK', 'TRUE', 'FALSE', 'CURRENT_TIMESTAMP', 'USING', 'BTREE',
]);

const SQL_TYPE_SET = new Set([
  'INT', 'INTEGER', 'BIGINT', 'SMALLINT', 'TINYINT', 'MEDIUMINT', 'SERIAL', 'BIGSERIAL',
  'VARCHAR', 'CHAR', 'NVARCHAR', 'TEXT', 'LONGTEXT', 'MEDIUMTEXT', 'CLOB', 'BLOB', 'BYTEA',
  'DECIMAL', 'NUMERIC', 'FLOAT', 'DOUBLE', 'REAL', 'BOOLEAN', 'BOOL', 'BIT',
  'DATE', 'TIME', 'DATETIME', 'TIMESTAMP', 'TIMESTAMPTZ', 'JSON', 'JSONB', 'UUID',
  'UINT8', 'UINT16', 'UINT32', 'UINT64', 'INT8', 'INT16', 'INT32', 'INT64', 'STRING', 'FLOAT32', 'FLOAT64',
]);

const TOKEN_RE = /(--[^\n]*|\/\*[\s\S]*?\*\/|'(?:''|[^'])*'|"(?:""|[^"])*"|`[^`]*`|\b\d+(?:\.\d+)?\b|\b[A-Za-z_][A-Za-z0-9_]*\b)/g;

function highlightLine(line: string, darkMode: boolean): React.ReactNode[] {
  if (!line) return ['\u200B'];
  const nodes: React.ReactNode[] = [];
  let lastIdx = 0;
  TOKEN_RE.lastIndex = 0;
  let match: RegExpExecArray | null;

  const colors = darkMode
    ? {
        comment: '#6a9955',
        string: '#ce9178',
        ident: '#9cdcfe',
        number: '#b5cea8',
        keyword: '#569cd6',
        type: '#4ec9b0',
      }
    : {
        comment: '#008000',
        string: '#a31515',
        ident: '#001080',
        number: '#098658',
        keyword: '#0000ff',
        type: '#267f99',
      };

  let keySeq = 0;
  while ((match = TOKEN_RE.exec(line)) !== null) {
    if (match.index > lastIdx) {
      nodes.push(line.slice(lastIdx, match.index));
    }
    const tok = match[0];
    let color = '';
    let fontWeight: number | undefined;

    if (tok.startsWith('--') || tok.startsWith('/*')) {
      color = colors.comment;
    } else if (tok.startsWith("'")) {
      color = colors.string;
    } else if (tok.startsWith('"') || tok.startsWith('`')) {
      color = colors.ident;
    } else if (/^\d/.test(tok)) {
      color = colors.number;
    } else {
      const upper = tok.toUpperCase();
      if (SQL_KEYWORD_SET.has(upper)) {
        color = colors.keyword;
        fontWeight = 600;
      } else if (SQL_TYPE_SET.has(upper)) {
        color = colors.type;
      }
    }

    if (color) {
      nodes.push(
        <span key={keySeq++} style={{ color, fontWeight }}>
          {tok}
        </span>
      );
    } else {
      nodes.push(tok);
    }
    lastIdx = match.index + tok.length;
  }

  if (lastIdx < line.length) {
    nodes.push(line.slice(lastIdx));
  }
  return nodes;
}

export const SqlCodeViewer: React.FC<SqlCodeViewerProps> = ({ code, darkMode = false, height = '100%' }) => {
  const renderedLines = useMemo(() => {
    const lines = (code || '-- DDL').split('\n');
    return lines.map((ln, idx) => ({
      lineNo: idx + 1,
      content: highlightLine(ln, darkMode),
    }));
  }, [code, darkMode]);

  return (
    <div
      style={{
        height,
        overflow: 'auto',
        background: darkMode ? '#1e1e1e' : '#ffffff',
        color: darkMode ? '#d4d4d4' : '#1f2328',
        fontFamily: "'JetBrains Mono', 'Cascadia Code', 'SF Mono', Consolas, Menlo, monospace",
        fontSize: 13,
        lineHeight: '20px',
        padding: '10px 0',
      }}
    >
      <table style={{ borderCollapse: 'collapse', width: '100%', minWidth: 'max-content' }}>
        <tbody>
          {renderedLines.map((row) => (
            <tr key={row.lineNo}>
              <td
                style={{
                  userSelect: 'none',
                  textAlign: 'right',
                  padding: '0 12px 0 10px',
                  width: 44,
                  color: darkMode ? '#858585' : '#999999',
                  fontSize: 12,
                  verticalAlign: 'top',
                }}
              >
                {row.lineNo}
              </td>
              <td style={{ paddingRight: 16, whiteSpace: 'pre' }}>{row.content}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
};
