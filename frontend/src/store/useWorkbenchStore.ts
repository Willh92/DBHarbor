import { create } from 'zustand';
import { api, clearAllConnectionAuthPrompts, getAuthToken, setAuthToken } from '../api/client';
import { detectInitialLocale, ensureLocaleLoaded, persistLocale, translateWithLocale, type AppLocale } from '../i18n';
import type { DriverMeta, LoginBrandingConfig, SavedConnection, UserRole, WorkspaceTab } from '../types';
import { clearAllEphemeralFileHandles } from '../utils/ephemeralFileHandles';
import { getRuntimeBootMeta, isDesktopRuntime } from '../utils/runtimeMode';

interface WorkbenchState {
  isDesktop: boolean;
  bootstrapped: boolean;
  token: string;
  username: string;
  displayName: string;
  role: UserRole;
  canCreateConn: boolean;
  totpEnabled: boolean;
  passwordChanged: boolean;
  recoveryCodesRemaining: number;
  loginBranding: LoginBrandingConfig | null;
  darkMode: boolean;
  locale: AppLocale;
  drivers: DriverMeta[];
  connections: SavedConnection[];
  tabs: WorkspaceTab[];
  activeTabKey: string;
  treeRefreshTick: number;
  requestedConnId: string;

  setAuth: (
    token: string,
    username: string,
    sec?: {
      displayName?: string;
      role?: UserRole;
      canCreateConn?: boolean;
      totpEnabled?: boolean;
      passwordChanged?: boolean;
      recoveryCodesRemaining?: number;
    }
  ) => void;
  setLoginBranding: (branding: LoginBrandingConfig) => void;
  setSecurityMeta: (sec: {
    displayName?: string;
    role?: UserRole;
    canCreateConn?: boolean;
    totpEnabled?: boolean;
    passwordChanged?: boolean;
    recoveryCodesRemaining?: number;
  }) => void;
  logout: (callBackend?: boolean) => void;
  toggleDarkMode: () => void;
  setLocale: (loc: AppLocale) => void;
  loadBootstrap: () => Promise<void>;
  reloadConnections: () => Promise<void>;
  bumpTreeRefresh: () => void;
  requestSelectConnection: (connId: string) => void;

  openTab: (tab: WorkspaceTab) => void;
  updateTab: (key: string, patch: Partial<WorkspaceTab>) => void;
  closeTab: (key: string) => void;
  closeOtherTabs: (keepKey: string) => void;
  closeLeftTabs: (pivotKey: string) => void;
  closeRightTabs: (pivotKey: string) => void;
  closeAllTabs: () => void;
  reorderTabs: (fromKey: string, toKey: string) => void;
  setActiveTab: (key: string) => void;
  closeConnectionTabs: (connId: string) => void;
}

const TAB_SESSION_STORAGE_KEY = 'dbharbor_open_tabs_v1';
const MAX_PERSISTED_TABS = 20;
const VALID_TAB_KINDS = new Set([
  'overview',
  'table_data',
  'table_structure',
  'sql_console',
  'redis_browser',
  'mongo_collection',
  'monitor',
  'db_users',
]);

function resolveSessionOwner(username: string, isDesktop: boolean): string {
  return username || (isDesktop ? 'local' : '');
}

function loadPersistedSession(): { owner: string; tabs: WorkspaceTab[]; activeTabKey: string } {
  try {
    const raw = localStorage.getItem(TAB_SESSION_STORAGE_KEY);
    if (!raw) return { owner: '', tabs: [], activeTabKey: '' };
    const parsed = JSON.parse(raw);
    if (!parsed || !Array.isArray(parsed.tabs)) return { owner: '', tabs: [], activeTabKey: '' };
    const owner = typeof parsed.owner === 'string' ? parsed.owner : '';
    const tabs: WorkspaceTab[] = parsed.tabs
      .filter(
        (t: any) =>
          t &&
          typeof t.key === 'string' &&
          typeof t.title === 'string' &&
          typeof t.connectionId === 'string' &&
          typeof t.dbType === 'string' &&
          VALID_TAB_KINDS.has(t.kind)
      )
      .slice(-MAX_PERSISTED_TABS)
      .map((t: any) => ({
        key: t.key,
        title: t.title,
        kind: t.kind,
        connectionId: t.connectionId,
        connName: typeof t.connName === 'string' ? t.connName : '',
        dbType: t.dbType,
        database: typeof t.database === 'string' ? t.database : '',
        schema: typeof t.schema === 'string' ? t.schema : undefined,
        table: typeof t.table === 'string' ? t.table : undefined,
        initialSQL: typeof t.initialSQL === 'string' ? t.initialSQL.slice(0, 4096) : undefined,
      }));
    const activeCandidate = typeof parsed.activeTabKey === 'string' ? parsed.activeTabKey : '';
    const activeTabKey = tabs.some((t) => t.key === activeCandidate) ? activeCandidate : tabs[0]?.key || '';
    return { owner, tabs, activeTabKey };
  } catch {
    return { owner: '', tabs: [], activeTabKey: '' };
  }
}

function persistTabSession(tabs: WorkspaceTab[], activeTabKey: string, owner: string): void {
  try {
    if (tabs.length === 0 || !owner) {
      localStorage.removeItem(TAB_SESSION_STORAGE_KEY);
      return;
    }
    const capped = tabs.slice(-MAX_PERSISTED_TABS).map((t) => ({
      ...t,
      initialSQL: t.initialSQL ? t.initialSQL.slice(0, 4096) : undefined,
    }));
    const validActive = capped.some((t) => t.key === activeTabKey) ? activeTabKey : capped[capped.length - 1]?.key || '';
    localStorage.setItem(TAB_SESSION_STORAGE_KEY, JSON.stringify({ owner, tabs: capped, activeTabKey: validActive }));
  } catch {
    // Ignore storage quota errors in restricted environments
  }
}

const initialIsDesktop = isDesktopRuntime();
const initialUsername = localStorage.getItem('dbharbor_username') || '';
const rawInitialSession = loadPersistedSession();
const isInitialSessionValid = initialIsDesktop
  ? !rawInitialSession.owner || rawInitialSession.owner === 'local'
  : Boolean(initialUsername && rawInitialSession.owner && rawInitialSession.owner === initialUsername);
const initialTabSession = isInitialSessionValid
  ? { tabs: rawInitialSession.tabs, activeTabKey: rawInitialSession.activeTabKey }
  : { tabs: [], activeTabKey: '' };

export const useWorkbenchStore = create<WorkbenchState>((set, get) => ({
  isDesktop: initialIsDesktop,
  bootstrapped: false,
  token: getAuthToken(),
  username: initialUsername,
  displayName: '',
  role: 'admin',
  canCreateConn: true,
  totpEnabled: false,
  passwordChanged: true,
  recoveryCodesRemaining: 0,
  loginBranding: null,
  darkMode: localStorage.getItem('dbharbor_theme')
    ? localStorage.getItem('dbharbor_theme') === 'dark'
    : Boolean(getRuntimeBootMeta().darkMode),
  locale: detectInitialLocale(),
  drivers: [],
  connections: [],
  tabs: initialTabSession.tabs,
  activeTabKey: initialTabSession.activeTabKey,
  treeRefreshTick: 0,
  requestedConnId: '',

  setAuth: (token, username, sec) => {
    const prevUsername = get().username;
    const switchedUser = Boolean(prevUsername && prevUsername !== username);
    const persisted = loadPersistedSession();
    const mismatchedPersisted = !get().isDesktop && Boolean(persisted.tabs.length > 0 && (!persisted.owner || persisted.owner !== username));
    if (switchedUser) {
      clearAllConnectionAuthPrompts();
      localStorage.removeItem(TAB_SESSION_STORAGE_KEY);
      void clearAllEphemeralFileHandles();
    } else if (mismatchedPersisted) {
      localStorage.removeItem(TAB_SESSION_STORAGE_KEY);
    }
    setAuthToken(token);
    localStorage.setItem('dbharbor_username', username);
    const nextRole: UserRole = sec?.role || 'admin';
    set({
      bootstrapped: false,
      token,
      username,
      displayName: sec?.displayName ?? '',
      role: nextRole,
      canCreateConn: sec?.canCreateConn ?? nextRole === 'admin',
      totpEnabled: sec?.totpEnabled ?? false,
      passwordChanged: sec?.passwordChanged ?? true,
      recoveryCodesRemaining: sec?.recoveryCodesRemaining ?? 0,
      ...(switchedUser
        ? {
            tabs: [],
            activeTabKey: '',
            connections: [],
            requestedConnId: '',
          }
        : mismatchedPersisted
          ? {
              tabs: [],
              activeTabKey: '',
            }
          : {}),
    });
  },

  setLoginBranding: (branding) => set({ loginBranding: branding }),

  setSecurityMeta: (sec) =>
    set((s) => ({
      displayName: sec.displayName ?? s.displayName,
      role: sec.role ?? s.role,
      canCreateConn: sec.canCreateConn ?? s.canCreateConn,
      totpEnabled: sec.totpEnabled ?? s.totpEnabled,
      passwordChanged: sec.passwordChanged ?? s.passwordChanged,
      recoveryCodesRemaining: sec.recoveryCodesRemaining ?? s.recoveryCodesRemaining,
    })),

  logout: (callBackend = true) => {
    if (callBackend && getAuthToken()) {
      api.logout().catch(() => {});
    }
    clearAllConnectionAuthPrompts();
    setAuthToken('');
    void clearAllEphemeralFileHandles();
    localStorage.removeItem('dbharbor_username');
    localStorage.removeItem(TAB_SESSION_STORAGE_KEY);
    set({
      bootstrapped: false,
      token: '',
      username: '',
      displayName: '',
      role: 'admin',
      canCreateConn: true,
      totpEnabled: false,
      passwordChanged: true,
      recoveryCodesRemaining: 0,
      connections: [],
      tabs: [],
      activeTabKey: '',
      requestedConnId: '',
    });
  },

  toggleDarkMode: () => {
    const next = !get().darkMode;
    localStorage.setItem('dbharbor_theme', next ? 'dark' : 'light');
    set({ darkMode: next });
  },

  setLocale: (loc) => {
    persistLocale(loc);
    ensureLocaleLoaded(loc).finally(() => {
      set({ locale: loc });
    });
  },

  loadBootstrap: async () => {
    const bootToken = get().token;
    const prevUsername = get().username;
    let currentOwner = 'local';
    try {
      if (get().isDesktop) {
        const [drivers, connections] = await Promise.all([api.listDrivers(), api.listConnections()]);
        currentOwner = 'local';
        set({
          username: 'local',
          displayName: 'Local Workspace',
          role: 'admin',
          canCreateConn: true,
          totpEnabled: false,
          passwordChanged: true,
          recoveryCodesRemaining: 0,
          drivers,
          connections,
        });
      } else {
        const [meInfo, drivers, connections] = await Promise.all([
          api.me(),
          api.listDrivers(),
          api.listConnections(),
        ]);
        currentOwner = meInfo.username;
        if (prevUsername && prevUsername !== meInfo.username) {
          void clearAllEphemeralFileHandles();
        }
        localStorage.setItem('dbharbor_username', meInfo.username);
        const nextRole: UserRole = meInfo.role || 'admin';
        set({
          username: meInfo.username,
          displayName: meInfo.displayName || '',
          role: nextRole,
          canCreateConn: meInfo.canCreateConn ?? nextRole === 'admin',
          totpEnabled: Boolean(meInfo.totpEnabled),
          passwordChanged: meInfo.passwordChanged !== false,
          recoveryCodesRemaining: meInfo.recoveryCodesRemaining || 0,
          drivers,
          connections,
        });
      }
      const { drivers, connections, isDesktop } = get();

      // Restore & prune persisted tabs only when session owner matches current authenticated identity
      const persisted = loadPersistedSession();
      const isOwnerValid = isDesktop
        ? !persisted.owner || persisted.owner === 'local'
        : Boolean(persisted.owner && persisted.owner === currentOwner);

      if (!isOwnerValid) {
        localStorage.removeItem(TAB_SESSION_STORAGE_KEY);
        if (get().tabs.length > 0 || get().activeTabKey) {
          set({ tabs: [], activeTabKey: '' });
        }
      } else {
        const connMap = new Map(connections.map((c) => [c.id, c]));
        const curTabs = get().tabs.length > 0 ? get().tabs : persisted.tabs;
        const curActive = get().activeTabKey || persisted.activeTabKey;
        if (curTabs.length > 0) {
          const validTabs = curTabs
            .filter((t) => connMap.has(t.connectionId))
            .map((t) => {
              const c = connMap.get(t.connectionId)!;
              return c.name !== t.connName ? { ...t, connName: c.name } : t;
            });
          const nextActive = validTabs.some((t) => t.key === curActive) ? curActive : validTabs[0]?.key || '';
          persistTabSession(validTabs, nextActive, currentOwner);
          set({ tabs: validTabs, activeTabKey: nextActive });
        }
      }

      // Auto-open the first unlocked connection's overview tab if no tab is open yet so users immediately see tables & tools
      // without triggering unsolicited credential prompts on unremembered connections at startup.
      if (get().tabs.length === 0 && connections.length > 0) {
        const first = connections.find((c) => c.rememberPassword !== false || c.unlocked);
        if (first) {
          const drv = drivers.find((d) => d.type === first.type);
          if (drv && (drv.category === 'relational' || drv.category === 'olap')) {
            const defaultDB =
              first.database || (first.type === 'sqlite' || first.type === 'duckdb' ? 'main' : '');
            if (defaultDB) {
              get().openTab({
                key: `overview:${first.id}:${defaultDB}:`,
                title: `${defaultDB} (${translateWithLocale(get().locale, 'Overview')})`,
                kind: 'overview',
                connectionId: first.id,
                connName: first.name,
                dbType: first.type,
                database: defaultDB,
                schema: '',
              });
            }
          }
        }
      }
    } finally {
      if (get().isDesktop || (bootToken && get().token === bootToken)) {
        set({ bootstrapped: true });
      }
    }
  },

  reloadConnections: async () => {
    const connections = await api.listConnections();
    set({ connections });
  },

  bumpTreeRefresh: () => set((s) => ({ treeRefreshTick: s.treeRefreshTick + 1 })),
  requestSelectConnection: (connId) => set({ requestedConnId: connId }),

  openTab: (tab) => {
    const owner = resolveSessionOwner(get().username, get().isDesktop);
    const existing = get().tabs.find((t) => t.key === tab.key);
    if (existing) {
      const nextPatch: Partial<WorkspaceTab> = {};
      if (tab.initialSQL && tab.initialSQL !== existing.initialSQL) {
        nextPatch.initialSQL = tab.initialSQL;
      }
      if (tab.database !== undefined && tab.database !== existing.database) {
        nextPatch.database = tab.database;
      }
      if (tab.schema !== undefined && tab.schema !== existing.schema) {
        nextPatch.schema = tab.schema;
      }
      if (tab.table !== undefined && tab.table !== existing.table) {
        nextPatch.table = tab.table;
      }
      if (tab.title && tab.title !== existing.title) {
        nextPatch.title = tab.title;
      }
      if (tab.connName && tab.connName !== existing.connName) {
        nextPatch.connName = tab.connName;
      }
      if (Object.keys(nextPatch).length > 0) {
        const nextTabs = get().tabs.map((t) => (t.key === tab.key ? { ...t, ...nextPatch } : t));
        persistTabSession(nextTabs, tab.key, owner);
        set({ tabs: nextTabs, activeTabKey: tab.key });
      } else {
        persistTabSession(get().tabs, tab.key, owner);
        set({ activeTabKey: tab.key });
      }
      return;
    }
    const nextTabs = [...get().tabs, tab];
    persistTabSession(nextTabs, tab.key, owner);
    set({ tabs: nextTabs, activeTabKey: tab.key });
  },

  updateTab: (key, patch) => {
    set((s) => {
      const nextKey = patch.key || key;
      const nextTabs = s.tabs.map((t) => (t.key === key ? { ...t, ...patch } : t));
      const nextActive = s.activeTabKey === key ? nextKey : s.activeTabKey;
      persistTabSession(nextTabs, nextActive, resolveSessionOwner(s.username, s.isDesktop));
      return {
        tabs: nextTabs,
        activeTabKey: nextActive,
      };
    });
  },

  closeTab: (key) => {
    const curTabs = get().tabs;
    const idx = curTabs.findIndex((t) => t.key === key);
    const nextTabs = curTabs.filter((t) => t.key !== key);
    let nextActive = get().activeTabKey;
    if (nextActive === key) {
      if (nextTabs.length === 0) {
        nextActive = '';
      } else if (idx > 0) {
        nextActive = nextTabs[idx - 1].key;
      } else {
        nextActive = nextTabs[0].key;
      }
    }
    persistTabSession(nextTabs, nextActive, resolveSessionOwner(get().username, get().isDesktop));
    set({ tabs: nextTabs, activeTabKey: nextActive });
  },

  closeOtherTabs: (keepKey) => {
    const nextTabs = get().tabs.filter((t) => t.key === keepKey);
    persistTabSession(nextTabs, keepKey, resolveSessionOwner(get().username, get().isDesktop));
    set({
      tabs: nextTabs,
      activeTabKey: keepKey,
    });
  },

  closeLeftTabs: (pivotKey) => {
    const cur = get().tabs;
    const idx = cur.findIndex((t) => t.key === pivotKey);
    if (idx <= 0) return;
    const nextTabs = cur.slice(idx);
    const activeStillValid = nextTabs.some((t) => t.key === get().activeTabKey);
    const nextActive = activeStillValid ? get().activeTabKey : pivotKey;
    persistTabSession(nextTabs, nextActive, resolveSessionOwner(get().username, get().isDesktop));
    set({
      tabs: nextTabs,
      activeTabKey: nextActive,
    });
  },

  closeRightTabs: (pivotKey) => {
    const cur = get().tabs;
    const idx = cur.findIndex((t) => t.key === pivotKey);
    if (idx < 0 || idx >= cur.length - 1) return;
    const nextTabs = cur.slice(0, idx + 1);
    const activeStillValid = nextTabs.some((t) => t.key === get().activeTabKey);
    const nextActive = activeStillValid ? get().activeTabKey : pivotKey;
    persistTabSession(nextTabs, nextActive, resolveSessionOwner(get().username, get().isDesktop));
    set({
      tabs: nextTabs,
      activeTabKey: nextActive,
    });
  },

  closeAllTabs: () => {
    persistTabSession([], '', resolveSessionOwner(get().username, get().isDesktop));
    set({ tabs: [], activeTabKey: '' });
  },

  reorderTabs: (fromKey, toKey) => {
    if (!fromKey || !toKey || fromKey === toKey) return;
    const cur = [...get().tabs];
    const fromIdx = cur.findIndex((t) => t.key === fromKey);
    const toIdx = cur.findIndex((t) => t.key === toKey);
    if (fromIdx < 0 || toIdx < 0) return;
    const [moved] = cur.splice(fromIdx, 1);
    cur.splice(toIdx, 0, moved);
    persistTabSession(cur, get().activeTabKey, resolveSessionOwner(get().username, get().isDesktop));
    set({ tabs: cur });
  },

  setActiveTab: (key) => {
    persistTabSession(get().tabs, key, resolveSessionOwner(get().username, get().isDesktop));
    set({ activeTabKey: key });
  },

  closeConnectionTabs: (connId) => {
    const nextTabs = get().tabs.filter((t) => t.connectionId !== connId);
    const activeStillValid = nextTabs.some((t) => t.key === get().activeTabKey);
    const nextActive = activeStillValid ? get().activeTabKey : nextTabs[0]?.key || '';
    persistTabSession(nextTabs, nextActive, resolveSessionOwner(get().username, get().isDesktop));
    set({
      tabs: nextTabs,
      activeTabKey: nextActive,
    });
  },
}));
