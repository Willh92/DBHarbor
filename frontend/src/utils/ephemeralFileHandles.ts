const EPHEMERAL_EXT_SET = new Set([
  '.sqlite',
  '.sqlite3',
  '.db',
  '.db3',
  '.s3db',
  '.duckdb',
  '.ddb',
]);

const handleRegistry = new Map<string, any>();

export function isSupportedSingleFileDBName(fileName: string): boolean {
  const clean = (fileName || '').trim().toLowerCase();
  const dotIdx = clean.lastIndexOf('.');
  if (dotIdx < 0) return false;
  return EPHEMERAL_EXT_SET.has(clean.slice(dotIdx));
}

export function supportsDirectFileHandleAccess(): boolean {
  return (
    typeof window !== 'undefined' &&
    Boolean(window.isSecureContext) &&
    typeof (window as any).showOpenFilePicker === 'function'
  );
}

export async function pickLocalSingleFileDBWithHandle(): Promise<{ file: File; handle?: any } | null> {
  const picker = (window as any).showOpenFilePicker;
  if (typeof picker !== 'function') {
    return null;
  }
  try {
    const handles = await picker({
      multiple: false,
      types: [
        {
          description: 'SQLite 3 / DuckDB Database',
          accept: {
            'application/octet-stream': [
              '.sqlite',
              '.sqlite3',
              '.db',
              '.db3',
              '.s3db',
              '.duckdb',
              '.ddb',
            ],
          },
        },
      ],
    });
    const handle = Array.isArray(handles) ? handles[0] : null;
    if (!handle) return null;
    const file: File = await handle.getFile();
    return { file, handle };
  } catch (err: any) {
    if (err?.name === 'AbortError') {
      return null;
    }
    throw err;
  }
}

export function bindEphemeralFileHandle(connId: string, handle: any): void {
  if (!connId || !handle) return;
  handleRegistry.set(connId, handle);
}

export function hasEphemeralFileHandle(connId: string): boolean {
  return Boolean(connId && handleRegistry.has(connId));
}

export function clearEphemeralFileHandle(connId?: string): void {
  if (!connId) {
    handleRegistry.clear();
    return;
  }
  handleRegistry.delete(connId);
}

export function removeEphemeralFileHandle(connId: string): void {
  if (!connId) return;
  handleRegistry.delete(connId);
}

export function clearAllEphemeralFileHandles(): void {
  handleRegistry.clear();
}

export async function writeBlobBackToLocalHandle(connId: string, blob: Blob): Promise<void> {
  const handle = handleRegistry.get(connId);
  if (!handle || typeof handle.createWritable !== 'function') {
    throw new Error('Local file handle is no longer available; please use Download DB File instead.');
  }
  if (typeof handle.queryPermission === 'function') {
    let perm = await handle.queryPermission({ mode: 'readwrite' });
    if (perm !== 'granted' && typeof handle.requestPermission === 'function') {
      perm = await handle.requestPermission({ mode: 'readwrite' });
    }
    if (perm !== 'granted') {
      throw new Error('Write permission to the local database file was not granted.');
    }
  }
  const writable = await handle.createWritable();
  await writable.write(blob);
  await writable.close();
}
