export function canonicalSerializeId(val: unknown): string {
  if (val === undefined) return 'undef';
  if (val === null) return 'null';
  if (typeof val === 'string') return `str:${val}`;
  if (typeof val === 'number') return `num:${Object.is(val, -0) ? '0' : String(val)}`;
  if (typeof val === 'boolean') return `bool:${val}`;
  if (Array.isArray(val)) return `arr:[${val.map(canonicalSerializeId).join(',')}]`;
  if (typeof val === 'object') {
    const rec = val as Record<string, unknown>;
    const keys = Object.keys(rec).sort();
    return `obj:{${keys.map((k) => `${JSON.stringify(k)}:${canonicalSerializeId(rec[k])}`).join(',')}}`;
  }
  return `other:${String(val)}`;
}

export function extractMongoMutationId(id: unknown): string | null {
  if (id === undefined || id === null) return null;
  if (typeof id === 'string') return id;
  if (typeof id === 'number' || typeof id === 'boolean') return String(id);
  if (typeof id === 'object' && !Array.isArray(id)) {
    const oid = (id as Record<string, unknown>).$oid;
    if (typeof oid === 'string' && oid.trim()) return oid.trim();
  }
  return null;
}

export function formatMongoIdDisplay(id: unknown): string {
  if (id === undefined) return '(no _id)';
  if (id === null) return 'null';
  if (typeof id === 'string') return id;
  if (typeof id === 'number' || typeof id === 'boolean') return String(id);
  if (typeof id === 'object' && !Array.isArray(id) && typeof (id as Record<string, unknown>).$oid === 'string') {
    return String((id as Record<string, unknown>).$oid);
  }
  try {
    return JSON.stringify(id);
  } catch {
    return String(id);
  }
}
