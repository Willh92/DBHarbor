import { useSyncExternalStore } from 'react';

export interface ResponsiveState {
  isMobile: boolean;
  isTablet: boolean;
  isCompactHeight: boolean;
}

const DEFAULT_STATE: ResponsiveState = {
  isMobile: false,
  isTablet: false,
  isCompactHeight: false,
};

let currentSnapshot: ResponsiveState = DEFAULT_STATE;
let initialized = false;
const subscribers = new Set<() => void>();

function computeSnapshot(): ResponsiveState {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
    return DEFAULT_STATE;
  }
  const isMobile = window.matchMedia('(max-width: 640px)').matches;
  const isTablet = !isMobile && window.matchMedia('(max-width: 960px)').matches;
  const isCompactHeight = window.matchMedia('(max-height: 700px)').matches;
  if (
    currentSnapshot.isMobile === isMobile &&
    currentSnapshot.isTablet === isTablet &&
    currentSnapshot.isCompactHeight === isCompactHeight
  ) {
    return currentSnapshot;
  }
  currentSnapshot = { isMobile, isTablet, isCompactHeight };
  return currentSnapshot;
}

function initMediaListeners() {
  if (initialized || typeof window === 'undefined' || typeof window.matchMedia !== 'function') {
    return;
  }
  initialized = true;
  currentSnapshot = computeSnapshot();

  const queries = [
    window.matchMedia('(max-width: 640px)'),
    window.matchMedia('(max-width: 960px)'),
    window.matchMedia('(max-height: 700px)'),
  ];

  const notify = () => {
    const prev = currentSnapshot;
    const next = computeSnapshot();
    if (prev !== next) {
      subscribers.forEach((fn) => fn());
    }
  };

  for (const mql of queries) {
    if (typeof mql.addEventListener === 'function') {
      mql.addEventListener('change', notify);
    } else if (typeof (mql as any).addListener === 'function') {
      (mql as any).addListener(notify);
    }
  }
}

function subscribe(callback: () => void): () => void {
  initMediaListeners();
  subscribers.add(callback);
  return () => {
    subscribers.delete(callback);
  };
}

function getSnapshot(): ResponsiveState {
  initMediaListeners();
  return currentSnapshot;
}

export function useResponsive(): ResponsiveState {
  return useSyncExternalStore(subscribe, getSnapshot, () => DEFAULT_STATE);
}
