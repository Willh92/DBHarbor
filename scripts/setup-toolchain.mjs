import { execFileSync, spawnSync } from 'node:child_process';
import fs from 'node:fs';
import https from 'node:https';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
export const ROOT_DIR = path.resolve(__dirname, '..');
const TOOLCHAIN_DIR = path.join(ROOT_DIR, '.toolchain');
const LOCAL_GO_ROOT = path.join(TOOLCHAIN_DIR, 'go');
const TARGET_GO_VERSION = '1.23.6';
const MIN_GO_MINOR = 22;

function getGoBinaryName() {
  return process.platform === 'win32' ? 'go.exe' : 'go';
}

function parseGoVersion(output) {
  const match = /go(\d+)\.(\d+)(?:\.(\d+))?/.exec(output);
  if (!match) return null;
  return {
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3] || 0),
    raw: match[0],
  };
}

function checkGoExecutable(goBin, requireLocalIntegrity = false) {
  try {
    if (!fs.existsSync(goBin) && goBin !== 'go' && goBin !== 'go.exe') {
      return null;
    }
    if (requireLocalIntegrity) {
      if (
        !fs.existsSync(path.join(LOCAL_GO_ROOT, 'src', 'runtime')) ||
        !fs.existsSync(path.join(LOCAL_GO_ROOT, 'pkg', 'include', 'textflag.h'))
      ) {
        return null;
      }
    }
    const out = execFileSync(goBin, ['version'], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] });
    const ver = parseGoVersion(out);
    if (ver && (ver.major > 1 || (ver.major === 1 && ver.minor >= MIN_GO_MINOR))) {
      return { bin: goBin, version: ver.raw };
    }
  } catch {
    return null;
  }
  return null;
}

export function resolveGoEnv() {
  const localBin = path.join(LOCAL_GO_ROOT, 'bin', getGoBinaryName());
  const localCheck = checkGoExecutable(localBin, true);
  if (localCheck) {
    const sep = path.delimiter;
    const env = {
      ...process.env,
      GOROOT: LOCAL_GO_ROOT,
      PATH: `${path.join(LOCAL_GO_ROOT, 'bin')}${sep}${process.env.PATH || ''}`,
      GOPROXY: process.env.GOPROXY || 'https://goproxy.cn,https://proxy.golang.org,direct',
    };
    return { goBin: localBin, env, version: localCheck.version };
  }

  const sysCheck = checkGoExecutable(getGoBinaryName(), false);
  if (sysCheck) {
    const env = {
      ...process.env,
      GOPROXY: process.env.GOPROXY || 'https://goproxy.cn,https://proxy.golang.org,direct',
    };
    return { goBin: sysCheck.bin, env, version: sysCheck.version };
  }

  return null;
}

function mapPlatformArch() {
  const platMap = { win32: 'windows', darwin: 'darwin', linux: 'linux' };
  const archMap = { x64: 'amd64', arm64: 'arm64' };
  const goos = platMap[os.platform()];
  const goarch = archMap[os.arch()];
  if (!goos || !goarch) {
    throw new Error(`Unsupported host platform/arch: ${os.platform()}/${os.arch()}`);
  }
  const ext = goos === 'windows' ? 'zip' : 'tar.gz';
  return { goos, goarch, ext, filename: `go${TARGET_GO_VERSION}.${goos}-${goarch}.${ext}` };
}

function downloadFile(urls, destPath) {
  return new Promise((resolve, reject) => {
    const tryUrl = (idx) => {
      if (idx >= urls.length) {
        reject(new Error('All Go SDK download mirrors failed.'));
        return;
      }
      const url = urls[idx];
      console.log(`[toolchain] Downloading ${url} ...`);
      const req = https.get(url, (res) => {
        if (res.statusCode && res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          const redirectUrl = new URL(res.headers.location, url).toString();
          res.resume();
          downloadFile([redirectUrl, ...urls.slice(idx + 1)], destPath).then(resolve).catch(reject);
          return;
        }
        if (res.statusCode !== 200) {
          res.resume();
          console.warn(`[toolchain] Mirror returned HTTP ${res.statusCode}, trying fallback...`);
          tryUrl(idx + 1);
          return;
        }
        const total = Number(res.headers['content-length'] || 0);
        let downloaded = 0;
        let lastPct = -1;
        const fileStream = fs.createWriteStream(destPath);

        res.on('data', (chunk) => {
          downloaded += chunk.length;
          if (total > 0) {
            const pct = Math.floor((downloaded / total) * 100);
            if (pct >= lastPct + 20) {
              lastPct = pct;
              console.log(`[toolchain] Progress: ${pct}% (${(downloaded / 1024 / 1024).toFixed(1)} MB)`);
            }
          }
        });

        res.on('error', (err) => {
          fileStream.destroy();
          fs.rmSync(destPath, { force: true });
          console.warn(`[toolchain] Stream error (${err.message}), trying fallback...`);
          tryUrl(idx + 1);
        });

        res.pipe(fileStream);
        fileStream.on('finish', () => {
          fileStream.close(() => {
            if (!res.complete || (total > 0 && downloaded !== total)) {
              fs.rmSync(destPath, { force: true });
              console.warn(`[toolchain] Incomplete download (${downloaded}/${total}), trying fallback...`);
              tryUrl(idx + 1);
              return;
            }
            resolve();
          });
        });
      });

      req.setTimeout(60000, () => {
        req.destroy(new Error('Socket idle timeout'));
      });

      req.on('error', (err) => {
        console.warn(`[toolchain] Mirror error (${err.message}), trying fallback...`);
        tryUrl(idx + 1);
      });
    };
    tryUrl(0);
  });
}

function extractArchive(archivePath, destDir, ext) {
  console.log(`[toolchain] Extracting ${path.basename(archivePath)} to ${destDir} ...`);
  if (ext === 'tar.gz') {
    const res = spawnSync('tar', ['-xzf', archivePath, '-C', destDir], { stdio: 'inherit' });
    if (res.status !== 0) throw new Error('Failed to extract tar.gz archive');
    return;
  }

  // Windows .zip: use .NET ZipFile::ExtractToDirectory for fast, strict integrity verification
  const psScript = `Add-Type -AssemblyName System.IO.Compression.FileSystem; [System.IO.Compression.ZipFile]::ExtractToDirectory('${archivePath.replace(/'/g, "''")}', '${destDir.replace(/'/g, "''")}')`;
  const psRes = spawnSync('powershell', ['-NoProfile', '-Command', psScript], { stdio: 'inherit' });
  if (psRes.status === 0) return;

  const tarRes = spawnSync('C:\\Windows\\System32\\tar.exe', ['-xf', archivePath, '-C', destDir], { stdio: 'inherit' });
  if (tarRes.status !== 0) throw new Error('Failed to extract zip archive');
}

export async function ensureGoToolchain() {
  const existing = resolveGoEnv();
  if (existing) {
    console.log(`[toolchain] Using Go toolchain: ${existing.version} (${existing.goBin})`);
    return existing;
  }

  const { filename, ext } = mapPlatformArch();
  fs.mkdirSync(TOOLCHAIN_DIR, { recursive: true });
  const archivePath = path.join(TOOLCHAIN_DIR, filename);

  if (fs.existsSync(LOCAL_GO_ROOT)) {
    fs.rmSync(LOCAL_GO_ROOT, { recursive: true, force: true });
  }

  const urls = [
    `https://golang.google.cn/dl/${filename}`,
    `https://go.dev/dl/${filename}`,
  ];
  await downloadFile(urls, archivePath);
  extractArchive(archivePath, TOOLCHAIN_DIR, ext);
  fs.rmSync(archivePath, { force: true });

  const resolved = resolveGoEnv();
  if (!resolved) {
    throw new Error('Go toolchain extraction completed, but binary/stdlib verification failed.');
  }
  console.log(`[toolchain] Ready: ${resolved.version} at ${resolved.goBin}`);
  return resolved;
}

const isMain = process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isMain) {
  await ensureGoToolchain();
  if (process.argv.includes('--install-fe')) {
    const feDir = path.join(ROOT_DIR, 'frontend');
    if (fs.existsSync(path.join(feDir, 'package.json'))) {
      console.log('[toolchain] Installing frontend dependencies...');
      const npmCmd = process.platform === 'win32' ? 'npm.cmd' : 'npm';
      const r = spawnSync(npmCmd, ['install'], { cwd: feDir, stdio: 'inherit', shell: process.platform === 'win32' });
      if (r.status !== 0) process.exit(r.status || 1);
    }
  }
}
