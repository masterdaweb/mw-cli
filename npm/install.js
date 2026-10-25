// Fetches the `mw` binary for this system from the GitHub release matching
// this package's version, checks it against the release's checksums.txt and
// puts it next to bin/mw.js. Run on install, and by bin/mw.js when it is
// missing (an install run with --ignore-scripts).
'use strict';
const crypto = require('crypto');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { execFileSync } = require('child_process');

const REPO = 'masterdaweb/mw-cli';
const version = require('./package.json').version;
const binDir = path.join(__dirname, 'bin');
const exe = path.join(binDir, process.platform === 'win32' ? 'mw-bin.exe' : 'mw-bin');

const OS = { darwin: 'darwin', linux: 'linux', win32: 'windows' }[process.platform];
const ARCH = { x64: 'amd64', arm64: 'arm64' }[process.arch];

async function get(url) {
  const res = await fetch(url, { redirect: 'follow' });
  if (!res.ok) throw new Error(`${url}: HTTP ${res.status}`);
  return Buffer.from(await res.arrayBuffer());
}

async function install() {
  if (!OS || !ARCH) throw new Error(`no mw build for ${process.platform}/${process.arch}`);
  const ext = OS === 'windows' ? 'zip' : 'tar.gz';
  const archive = `mw_${version}_${OS}_${ARCH}.${ext}`;
  const base = `https://github.com/${REPO}/releases/download/v${version}`;
  const [data, sums] = await Promise.all([get(`${base}/${archive}`), get(`${base}/checksums.txt`)]);
  const want = sums.toString('utf8').split('\n').map((l) => l.trim().split(/\s+/)).find((p) => p[1] === archive)?.[0];
  if (!want) throw new Error(`${archive} is not in the release's checksums.txt`);
  const got = crypto.createHash('sha256').update(data).digest('hex');
  if (got !== want) throw new Error(`${archive} does not match its checksum (got ${got}, want ${want})`);

  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'mw-'));
  try {
    const file = path.join(tmp, archive);
    fs.writeFileSync(file, data);
    // tar reads both on every system npm runs on (bsdtar on Windows 10+).
    execFileSync('tar', ['-xf', file, '-C', tmp], { stdio: 'ignore' });
    const name = OS === 'windows' ? 'mw.exe' : 'mw';
    fs.mkdirSync(binDir, { recursive: true });
    fs.copyFileSync(path.join(tmp, name), exe);
    fs.chmodSync(exe, 0o755);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
  return exe;
}

module.exports = { install, exe };

if (require.main === module) {
  install().catch((e) => {
    console.error(`mw: ${e.message}`);
    // A failed download must not fail `npm install`: bin/mw.js tries again.
    process.exit(0);
  });
}
