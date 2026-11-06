import { spawn } from 'node:child_process';
import path from 'node:path';
import { ensureGoToolchain, ROOT_DIR } from './setup-toolchain.mjs';

const BACKEND_DIR = path.join(ROOT_DIR, 'backend');
const FRONTEND_DIR = path.join(ROOT_DIR, 'frontend');

const { goBin, env } = await ensureGoToolchain();

console.log('[dev] Starting DBHarbor Go API (:8787) and Vite Frontend (:5173)...');

const backendProc = spawn(goBin, ['run', './cmd/dbharbor', '--port', '8787'], {
  cwd: BACKEND_DIR,
  env: { ...env, CGO_ENABLED: '0' },
  stdio: 'inherit',
});

const npmCmd = process.platform === 'win32' ? 'npm.cmd' : 'npm';
const frontendProc = spawn(npmCmd, ['run', 'dev'], {
  cwd: FRONTEND_DIR,
  stdio: 'inherit',
  shell: process.platform === 'win32',
});

function shutdown() {
  backendProc.kill('SIGTERM');
  frontendProc.kill('SIGTERM');
}

process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);

backendProc.on('exit', (code) => {
  if (code && code !== 0) {
    console.error(`[dev] Backend exited with code ${code}`);
    shutdown();
    process.exit(code);
  }
});

frontendProc.on('exit', (code) => {
  if (code && code !== 0) {
    console.error(`[dev] Frontend exited with code ${code}`);
    shutdown();
    process.exit(code);
  }
});
