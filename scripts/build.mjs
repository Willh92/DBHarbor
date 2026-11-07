import crypto from 'node:crypto';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import zlib from 'node:zlib';
import { ensureGoToolchain, ROOT_DIR } from './setup-toolchain.mjs';

const FRONTEND_DIR = path.join(ROOT_DIR, 'frontend');
const FRONTEND_DIST = path.join(FRONTEND_DIR, 'dist');
const BACKEND_DIR = path.join(ROOT_DIR, 'backend');
const EMBED_DIST = path.join(BACKEND_DIR, 'webdist', 'dist');
const DIST_DIR = path.join(ROOT_DIR, 'dist');

const ALL_TARGETS = [
  { goos: 'windows', goarch: 'amd64', ext: '.exe' },
  { goos: 'windows', goarch: 'arm64', ext: '.exe' },
  { goos: 'linux', goarch: 'amd64', ext: '' },
  { goos: 'linux', goarch: 'arm64', ext: '' },
  { goos: 'darwin', goarch: 'amd64', ext: '' },
  { goos: 'darwin', goarch: 'arm64', ext: '' },
];

const GZIP_EXTS = new Set(['.js', '.mjs', '.css', '.html', '.svg', '.json', '.ttf', '.webmanifest']);

function currentHostTarget() {
  const platMap = { win32: 'windows', darwin: 'darwin', linux: 'linux' };
  const archMap = { x64: 'amd64', arm64: 'arm64' };
  const goos = platMap[os.platform()] || 'linux';
  const goarch = archMap[os.arch()] || 'amd64';
  return { goos, goarch, ext: goos === 'windows' ? '.exe' : '' };
}

function copyDirRecursive(src, dest) {
  fs.rmSync(dest, { recursive: true, force: true });
  fs.mkdirSync(dest, { recursive: true });
  fs.cpSync(src, dest, { recursive: true });
}

function precompressEmbedDir(dir) {
  let rawBytes = 0;
  let finalBytes = 0;

  function walk(cur) {
    for (const entry of fs.readdirSync(cur, { withFileTypes: true })) {
      const fullPath = path.join(cur, entry.name);
      if (entry.isDirectory()) {
        walk(fullPath);
        continue;
      }
      if (entry.name === '.gitkeep' || entry.name.endsWith('.gz')) {
        continue;
      }
      if (entry.name.endsWith('.map')) {
        fs.rmSync(fullPath, { force: true });
        continue;
      }

      const buf = fs.readFileSync(fullPath);
      rawBytes += buf.length;

      const ext = path.extname(entry.name).toLowerCase();
      if (GZIP_EXTS.has(ext) && buf.length > 256) {
        const gz = zlib.gzipSync(buf, { level: 9 });
        if (gz.length < buf.length) {
          fs.writeFileSync(`${fullPath}.gz`, gz);
          fs.rmSync(fullPath, { force: true });
          finalBytes += gz.length;
          continue;
        }
      }
      finalBytes += buf.length;
    }
  }

  walk(dir);
  return { rawBytes, finalBytes };
}

function resolveVersionMeta() {
  const pkg = JSON.parse(fs.readFileSync(path.join(ROOT_DIR, 'package.json'), 'utf8'));
  const envVer = (process.env.DBHARBOR_BUILD_VERSION || '').trim().replace(/^[vV]/, '');
  const version = envVer || pkg.version || '1.0.2';
  const rawChannel = (process.env.DBHARBOR_BUILD_CHANNEL || '').trim().toLowerCase();
  const channel = rawChannel === 'preview' || rawChannel === 'release'
    ? rawChannel
    : version.includes('-')
      ? 'preview'
      : 'release';
  let gitCommit = 'release';
  try {
    const gitRes = spawnSync('git', ['rev-parse', '--short', 'HEAD'], { cwd: ROOT_DIR, encoding: 'utf8' });
    if (gitRes.status === 0 && gitRes.stdout.trim()) {
      gitCommit = gitRes.stdout.trim();
    }
  } catch {
    // ignore when git is unavailable
  }
  const buildTime = new Date().toISOString();
  return { version, channel, gitCommit, buildTime };
}

function syncChangelogAndInstallers(buildAll = false) {
  const changelogFiles = ['CHANGELOG.md', 'CHANGELOG.zh-CN.md', 'CHANGELOG.zh-TW.md', 'CHANGELOG.ja.md'];
  for (const fname of changelogFiles) {
    const src = path.join(ROOT_DIR, fname);
    const dest = path.join(BACKEND_DIR, 'internal', 'version', fname);
    if (fs.existsSync(src)) {
      fs.copyFileSync(src, dest);
    }
  }

  // Ensure scripts/install-service.sh strictly uses POSIX LF (\n) line endings
  const shSrc = path.join(ROOT_DIR, 'scripts', 'install-service.sh');
  if (fs.existsSync(shSrc)) {
    const lfContent = fs.readFileSync(shSrc, 'utf8').replace(/\r\n/g, '\n');
    fs.writeFileSync(shSrc, lfContent, 'utf8');
    fs.mkdirSync(DIST_DIR, { recursive: true });
    const shDest = path.join(DIST_DIR, 'install-service.sh');
    fs.writeFileSync(shDest, lfContent, { encoding: 'utf8', mode: 0o755 });
  }

  const psSrc = path.join(ROOT_DIR, 'scripts', 'install-service.ps1');
  if (fs.existsSync(psSrc)) {
    fs.mkdirSync(DIST_DIR, { recursive: true });
    fs.copyFileSync(psSrc, path.join(DIST_DIR, 'install-service.ps1'));
  }

  const licenseSrc = path.join(ROOT_DIR, 'LICENSE');
  if (fs.existsSync(licenseSrc)) {
    fs.mkdirSync(DIST_DIR, { recursive: true });
    fs.copyFileSync(licenseSrc, path.join(DIST_DIR, 'LICENSE'));
  }

  if (buildAll) {
    const deploySrc = path.join(ROOT_DIR, 'deploy');
    if (fs.existsSync(deploySrc)) {
      copyDirRecursive(deploySrc, path.join(DIST_DIR, 'deploy'));
    }
  }
}

function createMacOSAppBundle(desktopBinaryPath, goarch, version) {
  const bundleName = `DBHarbor-darwin-${goarch}.app`;
  const bundleDir = path.join(DIST_DIR, bundleName);
  const contentsDir = path.join(bundleDir, 'Contents');
  const macOSDir = path.join(contentsDir, 'MacOS');
  const resourcesDir = path.join(contentsDir, 'Resources');

  fs.rmSync(bundleDir, { recursive: true, force: true });
  fs.mkdirSync(macOSDir, { recursive: true });
  fs.mkdirSync(resourcesDir, { recursive: true });

  const binDest = path.join(macOSDir, 'dbharbor-desktop');
  fs.copyFileSync(desktopBinaryPath, binDest);
  try {
    fs.chmodSync(binDest, 0o755);
  } catch {
    // ignore on Windows host
  }

  const iconPng = path.join(FRONTEND_DIR, 'public', 'icon-512.png');
  if (fs.existsSync(iconPng)) {
    fs.copyFileSync(iconPng, path.join(resourcesDir, 'icon.png'));
  }

  const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key>
  <string>DBHarbor</string>
  <key>CFBundleDisplayName</key>
  <string>DBHarbor</string>
  <key>CFBundleIdentifier</key>
  <string>io.github.willh92.dbharbor.desktop</string>
  <key>CFBundleVersion</key>
  <string>${version}</string>
  <key>CFBundleShortVersionString</key>
  <string>${version}</string>
  <key>CFBundlePackageType</key>
  <string>APPL</string>
  <key>CFBundleExecutable</key>
  <string>dbharbor-desktop</string>
  <key>CFBundleIconFile</key>
  <string>icon.png</string>
  <key>NSHighResolutionCapable</key>
  <true/>
  <key>LSMinimumSystemVersion</key>
  <string>11.0</string>
</dict>
</plist>
`;
  fs.writeFileSync(path.join(contentsDir, 'Info.plist'), plist, 'utf8');
  return bundleName;
}

function sha256File(filePath) {
  const hash = crypto.createHash('sha256');
  hash.update(fs.readFileSync(filePath));
  return hash.digest('hex');
}

function prepareOutputBinaryPath(outPath) {
  if (process.platform !== 'win32') return;
  try {
    const staleOld = `${outPath}.old`;
    if (fs.existsSync(staleOld)) {
      fs.rmSync(staleOld, { force: true });
    }
  } catch {
    // ignore if previous .old is still running
  }
  if (!fs.existsSync(outPath)) return;
  try {
    fs.rmSync(outPath, { force: true });
  } catch {
    // Running .exe on Windows NTFS can be renamed out of the way while open
    const fallbackOld = `${outPath}.${Date.now()}.old`;
    fs.renameSync(outPath, fallbackOld);
  }
}

async function main() {
  const buildAll = process.argv.includes('--all');
  const skipFrontend = process.argv.includes('--skip-fe');
  const serverOnly = process.argv.includes('--server-only');
  const desktopOnly = process.argv.includes('--desktop-only');

  const { goBin, env } = await ensureGoToolchain();
  fs.mkdirSync(DIST_DIR, { recursive: true });
  syncChangelogAndInstallers(buildAll);

  if (!skipFrontend) {
    console.log('[build] Building frontend (React + TypeScript + Vite)...');
    const npmCmd = process.platform === 'win32' ? 'npm.cmd' : 'npm';
    if (!fs.existsSync(path.join(FRONTEND_DIR, 'node_modules'))) {
      const installRes = spawnSync(npmCmd, ['install'], {
        cwd: FRONTEND_DIR,
        stdio: 'inherit',
        shell: process.platform === 'win32',
      });
      if (installRes.status !== 0) process.exit(installRes.status || 1);
    }
    const feRes = spawnSync(npmCmd, ['run', 'build'], {
      cwd: FRONTEND_DIR,
      stdio: 'inherit',
      shell: process.platform === 'win32',
    });
    if (feRes.status !== 0) process.exit(feRes.status || 1);

    console.log('[build] Syncing & gzip-precompressing frontend assets into backend/webdist/dist ...');
    copyDirRecursive(FRONTEND_DIST, EMBED_DIST);
    const { rawBytes, finalBytes } = precompressEmbedDir(EMBED_DIST);
    fs.writeFileSync(path.join(EMBED_DIST, '.gitkeep'), '');
    console.log(
      `[build] Embedded web bundle: ${(rawBytes / 1024).toFixed(1)} KB raw -> ${(finalBytes / 1024).toFixed(1)} KB pre-compressed (.gz)`
    );
  } else if (
    fs.existsSync(path.join(FRONTEND_DIST, 'index.html')) &&
    !fs.existsSync(path.join(EMBED_DIST, 'index.html.gz')) &&
    !fs.existsSync(path.join(EMBED_DIST, 'index.html'))
  ) {
    console.log('[build] Syncing & gzip-precompressing prebuilt frontend/dist into backend/webdist/dist ...');
    copyDirRecursive(FRONTEND_DIST, EMBED_DIST);
    const { rawBytes, finalBytes } = precompressEmbedDir(EMBED_DIST);
    fs.writeFileSync(path.join(EMBED_DIST, '.gitkeep'), '');
    console.log(
      `[build] Embedded web bundle: ${(rawBytes / 1024).toFixed(1)} KB raw -> ${(finalBytes / 1024).toFixed(1)} KB pre-compressed (.gz)`
    );
  }

  const { version, channel, gitCommit, buildTime } = resolveVersionMeta();
  const baseLdflags = [
    '-s -w',
    `-X dbharbor/backend/internal/version.Version=${version}`,
    `-X dbharbor/backend/internal/version.Channel=${channel}`,
    `-X dbharbor/backend/internal/version.GitCommit=${gitCommit}`,
    `-X dbharbor/backend/internal/version.BuildTime=${buildTime}`,
  ];

  const targets = buildAll ? ALL_TARGETS : [currentHostTarget()];
  const checksumLines = [];

  const slimModPath = path.join(ROOT_DIR, '.toolchain', 'build-overlay', 'go.slim.mod');
  console.log('[build] Generating compressed Oracle charset Go slim modfile...');
  const ovRes = spawnSync(
    goBin,
    ['run', './cmd/gen-overlay', '-out', slimModPath],
    { cwd: BACKEND_DIR, env: { ...env, CGO_ENABLED: '0' }, stdio: 'inherit' }
  );
  if (ovRes.status !== 0) {
    console.error('[build] Failed generating Go slim modfile');
    process.exit(ovRes.status || 1);
  }

  for (const target of targets) {
    const buildEnv = {
      ...env,
      CGO_ENABLED: '0',
      GOOS: target.goos,
      GOARCH: target.goarch,
    };

    // 1. Web Server Edition (CLI / Service / Reverse Proxy / Unix Socket)
    if (!desktopOnly) {
      const outName = `dbharbor-${target.goos}-${target.goarch}${target.ext}`;
      const outPath = path.join(DIST_DIR, outName);
      prepareOutputBinaryPath(outPath);
      const serverLdflags = [...baseLdflags, '-X dbharbor/backend/internal/version.Edition=server'].join(' ');
      console.log(`[build] Compiling Server ${outName} v${version} (CGO_ENABLED=0 ${target.goos}/${target.goarch})...`);

      const res = spawnSync(
        goBin,
        ['build', '-trimpath', `-modfile=${slimModPath}`, `-ldflags=${serverLdflags}`, '-o', outPath, './cmd/dbharbor'],
        { cwd: BACKEND_DIR, env: buildEnv, stdio: 'inherit' }
      );
      if (res.status !== 0) {
        console.error(`[build] Failed compiling ${outName}`);
        process.exit(res.status || 1);
      }

      const digest = sha256File(outPath);
      const sizeMB = (fs.statSync(outPath).size / 1024 / 1024).toFixed(2);
      checksumLines.push(`${digest}  ${outName}`);
      console.log(`[build] OK -> dist/${outName} (${sizeMB} MB)`);
    }

    // 2. Native Desktop GUI Edition (Pure Local Mode, No Account System, Direct Window)
    if (!serverOnly) {
      const deskName = `dbharbor-desktop-${target.goos}-${target.goarch}${target.ext}`;
      const deskPath = path.join(DIST_DIR, deskName);
      prepareOutputBinaryPath(deskPath);
      const deskFlags = [...baseLdflags, '-X dbharbor/backend/internal/version.Edition=desktop'];
      if (target.goos === 'windows') {
        deskFlags.push('-H=windowsgui');
      }
      console.log(`[build] Compiling Desktop GUI ${deskName} v${version} (CGO_ENABLED=0 ${target.goos}/${target.goarch})...`);

      const deskRes = spawnSync(
        goBin,
        ['build', '-trimpath', `-modfile=${slimModPath}`, `-ldflags=${deskFlags.join(' ')}`, '-o', deskPath, './cmd/dbharbor-desktop'],
        { cwd: BACKEND_DIR, env: buildEnv, stdio: 'inherit' }
      );
      if (deskRes.status !== 0) {
        console.error(`[build] Failed compiling ${deskName}`);
        process.exit(deskRes.status || 1);
      }

      const deskDigest = sha256File(deskPath);
      const deskSizeMB = (fs.statSync(deskPath).size / 1024 / 1024).toFixed(2);
      checksumLines.push(`${deskDigest}  ${deskName}`);
      console.log(`[build] OK -> dist/${deskName} (${deskSizeMB} MB)`);

      if (target.goos === 'darwin') {
        const bundleName = createMacOSAppBundle(deskPath, target.goarch, version);
        console.log(`[build] OK -> dist/${bundleName} (macOS Finder .app bundle)`);
      }
    }
  }

  if (buildAll) {
    const sumPath = path.join(DIST_DIR, 'SHA256SUMS.txt');
    fs.writeFileSync(sumPath, checksumLines.join('\n') + '\n', 'utf8');
    console.log(`[build] Wrote checksums to dist/SHA256SUMS.txt`);
  }
}

await main();
