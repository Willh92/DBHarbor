import React from 'react';
import { DatabaseOutlined, SafetyCertificateOutlined } from '@ant-design/icons';
import type { LoginBrandingConfig, LoginIconMode } from '../../types';
import { BrandLogo } from './BrandLogo';

export const DEFAULT_LOGIN_BRANDING: LoginBrandingConfig = {
  pageTitle: '',
  iconMode: 'shield',
  customIconDataUrl: '',
  heading: '',
  subtitle: '',
};

export function readBrandingImageAsDataUrl(file: File, maxBytes = 128 * 1024): Promise<string> {
  return new Promise((resolve, reject) => {
    const allowed = new Set([
      'image/png',
      'image/jpeg',
      'image/webp',
      'image/gif',
      'image/svg+xml',
    ]);
    if (!allowed.has(file.type)) {
      reject(new Error('Unsupported image format (use PNG, JPG, WebP, GIF, or SVG)'));
      return;
    }
    if (file.size > maxBytes) {
      reject(new Error('Image file must not exceed 128 KB'));
      return;
    }
    const reader = new FileReader();
    reader.onload = () => {
      const res = typeof reader.result === 'string' ? reader.result : '';
      if (!res.startsWith('data:image/')) {
        reject(new Error('Failed to encode image file'));
        return;
      }
      resolve(res);
    };
    reader.onerror = () => reject(new Error('Failed to read image file'));
    reader.readAsDataURL(file);
  });
}

interface EmblemProps {
  iconMode?: LoginIconMode;
  customIconDataUrl?: string;
  size?: number;
  darkMode?: boolean;
}

export const LoginBrandEmblem: React.FC<EmblemProps> = ({
  iconMode = 'shield',
  customIconDataUrl = '',
  size = 54,
  darkMode = false,
}) => {
  if (iconMode === 'none') {
    return null;
  }

  if (iconMode === 'custom' && customIconDataUrl) {
    return (
      <div
        style={{
          width: size,
          height: size,
          margin: '0 auto',
          borderRadius: Math.round(size * 0.24),
          overflow: 'hidden',
          display: 'inline-flex',
          alignItems: 'center',
          justifyContent: 'center',
          background: darkMode ? 'rgba(255,255,255,0.04)' : 'rgba(15,23,42,0.03)',
          border: darkMode ? '1px solid rgba(255,255,255,0.1)' : '1px solid rgba(15,23,42,0.08)',
        }}
      >
        <img
          src={customIconDataUrl}
          alt=""
          style={{ width: '100%', height: '100%', objectFit: 'contain', display: 'block' }}
        />
      </div>
    );
  }

  if (iconMode === 'brand') {
    return <BrandLogo size={size} glow />;
  }

  const iconSize = Math.round(size * 0.48);
  return (
    <div
      style={{
        width: size,
        height: size,
        margin: '0 auto',
        borderRadius: Math.round(size * 0.26),
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        color: '#ffffff',
        fontSize: iconSize,
        background: 'linear-gradient(135deg, #2563eb 0%, #1d4ed8 58%, #0f766e 100%)',
        boxShadow: darkMode
          ? '0 10px 26px rgba(37, 99, 235, 0.34), inset 0 1px 0 rgba(255,255,255,0.22)'
          : '0 10px 24px rgba(37, 99, 235, 0.24), inset 0 1px 0 rgba(255,255,255,0.28)',
      }}
    >
      {iconMode === 'database' ? <DatabaseOutlined /> : <SafetyCertificateOutlined />}
    </div>
  );
};
