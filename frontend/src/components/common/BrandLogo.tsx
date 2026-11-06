import React, { useId } from 'react';

export interface BrandLogoProps {
  size?: number;
  style?: React.CSSProperties;
  className?: string;
  glow?: boolean;
}

export const BrandLogo: React.FC<BrandLogoProps> = ({ size = 32, style, className, glow = false }) => {
  const rawId = useId().replace(/:/g, '');
  const bgGradId = `dbh-bg-${rawId}`;
  const topGlowId = `dbh-glow-${rawId}`;
  const rimGradId = `dbh-rim-${rawId}`;
  const cylSideGradId = `dbh-cyl-${rawId}`;
  const anchorGradId = `dbh-anchor-${rawId}`;
  const squircleClipId = `dbh-sq-${rawId}`;
  const tier2ClipId = `dbh-t2-${rawId}`;

  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      viewBox="0 0 512 512"
      width={size}
      height={size}
      className={className}
      role="img"
      aria-label="DBHarbor Logo"
      style={{
        display: 'inline-block',
        verticalAlign: 'middle',
        flexShrink: 0,
        filter: glow ? 'drop-shadow(0 8px 20px rgba(22, 119, 255, 0.32))' : undefined,
        ...style,
      }}
    >
      <defs>
        <linearGradient id={bgGradId} x1="0" y1="0" x2="512" y2="512" gradientUnits="userSpaceOnUse">
          <stop offset="0%" stopColor="#071A3E" />
          <stop offset="48%" stopColor="#1677FF" />
          <stop offset="100%" stopColor="#13C2C2" />
        </linearGradient>

        <radialGradient id={topGlowId} cx="384" cy="88" r="340" gradientUnits="userSpaceOnUse">
          <stop offset="0%" stopColor="#69E2FF" stopOpacity="0.35" />
          <stop offset="100%" stopColor="#1677FF" stopOpacity="0" />
        </radialGradient>

        <linearGradient id={rimGradId} x1="0" y1="0" x2="512" y2="512" gradientUnits="userSpaceOnUse">
          <stop offset="0%" stopColor="#FFFFFF" stopOpacity="0.40" />
          <stop offset="50%" stopColor="#FFFFFF" stopOpacity="0.10" />
          <stop offset="100%" stopColor="#FFFFFF" stopOpacity="0.18" />
        </linearGradient>

        <linearGradient id={cylSideGradId} x1="140" y1="0" x2="372" y2="0" gradientUnits="userSpaceOnUse">
          <stop offset="0%" stopColor="#91D5FF" />
          <stop offset="32%" stopColor="#E6F7FF" />
          <stop offset="68%" stopColor="#BAE7FF" />
          <stop offset="100%" stopColor="#5CDBD3" />
        </linearGradient>

        <linearGradient id={anchorGradId} x1="256" y1="236" x2="256" y2="442" gradientUnits="userSpaceOnUse">
          <stop offset="0%" stopColor="#FFFFFF" />
          <stop offset="65%" stopColor="#F0FBFF" />
          <stop offset="100%" stopColor="#B5F5EC" />
        </linearGradient>

        <clipPath id={squircleClipId}>
          <rect x="0" y="0" width="512" height="512" rx="116" />
        </clipPath>

        <clipPath id={tier2ClipId}>
          <path d="M 140 162 A 116 27 0 0 0 372 162 L 372 200 A 116 27 0 0 1 140 200 Z" />
        </clipPath>
      </defs>

      <g clipPath={`url(#${squircleClipId})`}>
        <rect x="0" y="0" width="512" height="512" rx="116" fill={`url(#${bgGradId})`} />
        <rect x="0" y="0" width="512" height="512" fill={`url(#${topGlowId})`} />

        <path
          d="M 0 384 C 132 348, 226 420, 358 384 C 434 363, 478 372, 512 388 L 512 512 L 0 512 Z"
          fill="#FFFFFF"
          fillOpacity="0.06"
        />
        <path
          d="M 0 430 C 150 400, 290 460, 422 424 C 464 413, 492 416, 512 426 L 512 512 L 0 512 Z"
          fill="#041026"
          fillOpacity="0.16"
        />

        {/* Database Tier 1 */}
        <path d="M 140 106 L 140 144 A 116 27 0 0 0 372 144 L 372 106 Z" fill={`url(#${cylSideGradId})`} />
        <ellipse cx="256" cy="106" rx="116" ry="27" fill="#FFFFFF" />
        <ellipse cx="256" cy="106" rx="92" ry="18" fill="none" stroke="#E6F4FF" strokeWidth="2.5" />
        <circle cx="326" cy="138" r="6.5" fill="#0958D9" />
        <circle cx="302" cy="142" r="6.5" fill="#08979C" />

        {/* Database Tier 2 */}
        <g clipPath={`url(#${tier2ClipId})`}>
          <path d="M 140 162 A 116 27 0 0 0 372 162 L 372 200 A 116 27 0 0 1 140 200 Z" fill={`url(#${cylSideGradId})`} />
          <path d="M 140 162 A 116 27 0 0 0 372 162" fill="none" stroke="#FFFFFF" strokeWidth="6" />
        </g>
        <circle cx="326" cy="194" r="6.5" fill="#0958D9" />
        <circle cx="302" cy="198" r="6.5" fill="#08979C" />

        {/* Unified Harbor Anchor */}
        <circle cx="256" cy="258" r="17" fill="none" stroke={`url(#${anchorGradId})`} strokeWidth="13" />
        <rect x="240" y="273" width="32" height="144" rx="4" fill={`url(#${anchorGradId})`} />
        <path
          d="M 118 312 C 130 382, 188 414, 256 414 C 324 414, 382 382, 394 312"
          fill="none"
          stroke={`url(#${anchorGradId})`}
          strokeWidth="32"
          strokeLinecap="round"
        />
        <path
          d="M 226 410 L 256 438 L 286 410 Z"
          fill={`url(#${anchorGradId})`}
          stroke={`url(#${anchorGradId})`}
          strokeWidth="8"
          strokeLinejoin="round"
        />
        <path
          d="M 110 266 L 82 324 L 118 308 L 156 316 Z"
          fill={`url(#${anchorGradId})`}
          stroke={`url(#${anchorGradId})`}
          strokeWidth="10"
          strokeLinejoin="round"
        />
        <path
          d="M 402 266 L 356 316 L 394 308 L 430 324 Z"
          fill={`url(#${anchorGradId})`}
          stroke={`url(#${anchorGradId})`}
          strokeWidth="10"
          strokeLinejoin="round"
        />

        <rect x="5" y="5" width="502" height="502" rx="111" fill="none" stroke={`url(#${rimGradId})`} strokeWidth="4" />
      </g>
    </svg>
  );
};
