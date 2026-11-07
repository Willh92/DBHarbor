import { useEffect } from 'react';

const POPUP_SELECTOR =
  '.ant-dropdown, .ant-dropdown-menu-submenu-popup, .ant-popover, .ant-popconfirm, .ant-select-dropdown, .ant-picker-dropdown, .ant-tooltip';

const VIEWPORT_MARGIN = 8;

/**
 * Global viewport boundary guard for all Ant Design / rc-trigger portal popups across desktop & mobile.
 *
 * Uses idempotent `marginLeft` / `marginTop` offsets computed from the raw unclamped `getBoundingClientRect()`
 * so it never conflicts with `rc-trigger`'s own `left` / `right` / `top` / `bottom` / `inset` positioning or
 * `transform: none` anti-jitter rules, and executes in MutationObserver microtask timing prior to paint.
 */
export function useViewportPopupClamp(): void {
  useEffect(() => {
    if (typeof window === 'undefined' || typeof document === 'undefined') return;

    const internalMutating = new WeakSet<HTMLElement>();

    const clearClampStyles = (el: HTMLElement) => {
      if (el.dataset.dbharborClamped === '1') {
        el.style.removeProperty('margin-left');
        el.style.removeProperty('margin-top');
        delete el.dataset.dbharborClamped;
      }
      const arrow = el.querySelector<HTMLElement>('.ant-popover-arrow, .ant-dropdown-arrow, .ant-tooltip-arrow');
      if (arrow && arrow.dataset.dbharborClamped === '1') {
        arrow.style.removeProperty('margin-left');
        delete arrow.dataset.dbharborClamped;
      }
    };

    const clampPopup = (el: HTMLElement) => {
      if (internalMutating.has(el)) return;

      const cls = typeof el.className === 'string' ? el.className : '';
      if (cls.includes('-hidden')) {
        internalMutating.add(el);
        clearClampStyles(el);
        queueMicrotask(() => internalMutating.delete(el));
        return;
      }

      const vw = document.documentElement.clientWidth || window.innerWidth;
      const vh = document.documentElement.clientHeight || window.innerHeight;
      if (vw <= 0 || vh <= 0) return;

      const curML = parseFloat(el.style.marginLeft || '0') || 0;
      const curMT = parseFloat(el.style.marginTop || '0') || 0;

      const rect = el.getBoundingClientRect();
      const rawLeft = rect.left - curML;
      const rawTop = rect.top - curMT;

      // Ignore unmeasured or off-screen parked rc-trigger containers (e.g. -9999px / -1000vw)
      if (
        rect.width <= 0 ||
        rect.height <= 0 ||
        rawLeft < -450 ||
        rawTop < -450 ||
        rawLeft > vw + 450 ||
        rawTop > vh + 450
      ) {
        return;
      }

      let clampedLeft = rawLeft;
      if (rect.width > vw - VIEWPORT_MARGIN * 2) {
        clampedLeft = VIEWPORT_MARGIN;
      } else if (rawLeft < VIEWPORT_MARGIN) {
        clampedLeft = VIEWPORT_MARGIN;
      } else if (rawLeft + rect.width > vw - VIEWPORT_MARGIN) {
        clampedLeft = vw - VIEWPORT_MARGIN - rect.width;
      }

      let clampedTop = rawTop;
      if (rect.height > vh - VIEWPORT_MARGIN * 2) {
        clampedTop = VIEWPORT_MARGIN;
      } else if (rawTop < VIEWPORT_MARGIN) {
        clampedTop = VIEWPORT_MARGIN;
      } else if (rawTop + rect.height > vh - VIEWPORT_MARGIN) {
        clampedTop = vh - VIEWPORT_MARGIN - rect.height;
      }

      const desiredML = Math.round(clampedLeft - rawLeft);
      const desiredMT = Math.round(clampedTop - rawTop);

      if (Math.abs(desiredML - Math.round(curML)) < 1 && Math.abs(desiredMT - Math.round(curMT)) < 1) {
        return;
      }

      internalMutating.add(el);
      try {
        if (desiredML === 0) {
          el.style.removeProperty('margin-left');
        } else {
          el.style.marginLeft = `${desiredML}px`;
        }
        if (desiredMT === 0) {
          el.style.removeProperty('margin-top');
        } else {
          el.style.marginTop = `${desiredMT}px`;
        }

        if (desiredML !== 0 || desiredMT !== 0) {
          el.dataset.dbharborClamped = '1';
        } else {
          delete el.dataset.dbharborClamped;
        }

        const arrow = el.querySelector<HTMLElement>('.ant-popover-arrow, .ant-dropdown-arrow, .ant-tooltip-arrow');
        if (arrow) {
          if (desiredML === 0) {
            arrow.style.removeProperty('margin-left');
            delete arrow.dataset.dbharborClamped;
          } else {
            const curArrowML = parseFloat(arrow.style.marginLeft || '0') || 0;
            const arrowRect = arrow.getBoundingClientRect();
            const rawArrowRelX = arrowRect.left - curArrowML - rawLeft;
            const minRelX = 12;
            const maxRelX = Math.max(minRelX, rect.width - 12 - arrowRect.width);
            const targetArrowRelX = Math.max(minRelX, Math.min(maxRelX, rawArrowRelX - desiredML));
            const arrowOffset = Math.round(targetArrowRelX - rawArrowRelX);
            arrow.style.marginLeft = `${arrowOffset}px`;
            arrow.dataset.dbharborClamped = '1';
          }
        }
      } finally {
        queueMicrotask(() => {
          internalMutating.delete(el);
        });
      }
    };

    const inspectNode = (node: Node) => {
      if (!(node instanceof HTMLElement)) return;
      if (node.matches(POPUP_SELECTOR)) {
        clampPopup(node);
      }
      node.querySelectorAll<HTMLElement>(POPUP_SELECTOR).forEach(clampPopup);
    };

    const clampAllOpenPopups = () => {
      document.querySelectorAll<HTMLElement>(POPUP_SELECTOR).forEach(clampPopup);
    };

    const observer = new MutationObserver((mutations) => {
      for (let i = 0; i < mutations.length; i++) {
        const m = mutations[i];
        if (m.type === 'attributes' && m.target instanceof HTMLElement) {
          if (m.target.matches(POPUP_SELECTOR)) {
            clampPopup(m.target);
          } else {
            const host = m.target.closest<HTMLElement>(POPUP_SELECTOR);
            if (host) clampPopup(host);
          }
        } else if (m.type === 'childList') {
          m.addedNodes.forEach(inspectNode);
        }
      }
    });

    observer.observe(document.body, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ['style', 'class'],
    });

    window.addEventListener('resize', clampAllOpenPopups, { passive: true });
    return () => {
      observer.disconnect();
      window.removeEventListener('resize', clampAllOpenPopups);
    };
  }, []);
}
