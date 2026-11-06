import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { ensureGoToolchain, ROOT_DIR } from './setup-toolchain.mjs';

const BACKEND_DIR = path.join(ROOT_DIR, 'backend');
const FRONTEND_DIR = path.join(ROOT_DIR, 'frontend');

const { goBin, env } = await ensureGoToolchain();

console.log('[test] 1/2 Running Go unit & integration tests (CGO_ENABLED=0)...');
const goRes = spawnSync(goBin, ['test', './...', '-v'], {
  cwd: BACKEND_DIR,
  env: { ...env, CGO_ENABLED: '0' },
  stdio: 'inherit',
});
if (goRes.status !== 0) {
  process.exit(goRes.status || 1);
}

if (fs.existsSync(path.join(FRONTEND_DIR, 'package.json'))) {
  console.log('[test] 2/2 Running Frontend TypeScript & Vite production build check...');
  const npmCmd = process.platform === 'win32' ? 'npm.cmd' : 'npm';
  const feRes = spawnSync(npmCmd, ['run', 'build'], {
    cwd: FRONTEND_DIR,
    stdio: 'inherit',
    shell: process.platform === 'win32',
  });
  if (feRes.status !== 0) {
    process.exit(feRes.status || 1);
  }
}

console.log('[test] All backend and frontend checks passed!');
