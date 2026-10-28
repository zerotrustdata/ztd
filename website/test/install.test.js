// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { delimiter, join, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const installer = fileURLToPath(new URL('../static/ztd.sh', import.meta.url));
const HOST_PATH = (process.env.PATH || '').split(delimiter);

// Host tools the installer may call directly. Everything else on the host is hidden
// from the installer PATH so that stubs decide which checksum tool exists.
const REAL_TOOLS = ['awk', 'tr', 'mktemp', 'install', 'rm', 'cat', 'sed', 'sleep'];

function isFile(candidate) {
  try {
    return statSync(candidate).isFile();
  } catch {
    return false;
  }
}

function hostTool(name) {
  for (const directory of HOST_PATH) {
    const candidate = join(directory, name);
    if (isFile(candidate)) return candidate;
    if (process.platform === 'win32' && !name.toLowerCase().endsWith('.exe')) {
      const exeCandidate = join(directory, `${name}.exe`);
      if (isFile(exeCandidate)) return exeCandidate;
    }
  }
  return null;
}

const SHELL = hostTool('sh') || '/bin/sh';

function linkOrCopy(src, dst) {
  if (process.platform === 'win32') {
    writeFileSync(dst, `#!/bin/sh\nexec "${src.replace(/\\/g, '/')}" "$@"\n`);
    chmodSync(dst, 0o755);
  } else {
    symlinkSync(src, dst);
  }
}

function sha256Hex(content) {
  return createHash('sha256').update(content).digest('hex');
}

const STUB_CURL = `#!/bin/sh
out=""
url=""
while [ $# -gt 0 ]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    -*) shift ;;
    *) url="$1"; shift ;;
  esac
done
echo "$url" >> "$ZTD_TEST_LOG/curl.log"
case "$url" in
  */latest.json) kind=latest ;;
  *.sha256) kind=checksum ;;
  *) kind=binary ;;
esac
if [ "$ZTD_TEST_FAIL" = "$kind" ]; then
  exit 22
fi
[ -n "$out" ] || exit 2
[ -f "$ZTD_TEST_SERVE/$kind" ] || exit 22
cat "$ZTD_TEST_SERVE/$kind" > "$out"
`;

const STUB_UNAME = `#!/bin/sh
case "$1" in
  -s) echo "$ZTD_TEST_OS" ;;
  -m) echo "$ZTD_TEST_ARCH" ;;
  *) exit 2 ;;
esac
`;

// Records any attempt to escalate privileges and always fails, so tests can prove sudo is never used.
const STUB_SUDO = `#!/bin/sh
echo "sudo $*" >> "$ZTD_TEST_LOG/sudo.log"
exit 1
`;

// Emulates "shasum -a 256 FILE" with the digest the test expects for the served binary.
const STUB_SHASUM = `#!/bin/sh
echo "shasum $*" >> "$ZTD_TEST_LOG/shasum.log"
[ "$1" = -a ] && [ "$2" = 256 ] || exit 2
printf '%s  %s\\n' "$ZTD_TEST_DIGEST" "$3"
`;

// Makes an isolated PATH: stubs plus symlinks to the host tools the installer needs.
function makeBin(root, { shasum, sha256sum }) {
  const bin = join(root, 'bin');
  mkdirSync(bin);
  const writeStub = (name, body) => {
    const path = join(bin, name);
    writeFileSync(path, body);
    chmodSync(path, 0o755);
  };
  writeStub('curl', STUB_CURL);
  writeStub('uname', STUB_UNAME);
  writeStub('sudo', STUB_SUDO);
  if (shasum) writeStub('shasum', STUB_SHASUM);
  if (sha256sum) {
    const real = hostTool('sha256sum');
    if (!real) throw new Error('sha256sum is required on the host for this test');
    linkOrCopy(real, join(bin, 'sha256sum'));
  }
  for (const name of REAL_TOOLS) {
    const real = hostTool(name);
    if (real) linkOrCopy(real, join(bin, name));
  }
  return bin;
}

// Builds a fixture: a served binary, a served checksum, a work directory, and a TMPDIR the
// installer must leave empty. Options override the defaults to simulate tampering or bad servers.
function setup(t, options = {}) {
  const root = mkdtempSync(join(tmpdir(), 'ztd-install-test-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const serve = join(root, 'serve');
  const log = join(root, 'log');
  const scratch = join(root, 'tmp with spaces');
  const work = join(root, 'work dir with spaces');
  for (const dir of [serve, log, scratch, work]) mkdirSync(dir, { recursive: true });

  const binary = options.binary ?? Buffer.from('#!/bin/sh\necho ztd\n');
  const digest = sha256Hex(binary);
  writeFileSync(join(serve, 'binary'), binary);
  writeFileSync(join(serve, 'checksum'), options.checksum ?? `${digest}  ztd-linux-amd64\n`);
  // The published pointer. Passing latest: null omits it, simulating a missing latest.json (served as 404).
  if (options.latest !== null) writeFileSync(join(serve, 'latest'), options.latest ?? '{"version":"2.3.2"}\n');
  const bin = makeBin(root, { shasum: options.shasum ?? false, sha256sum: options.sha256sum ?? true });

  const env = {
    PATH: bin,
    TMPDIR: scratch,
    ZTD_TEST_LOG: log,
    ZTD_TEST_SERVE: serve,
    ZTD_TEST_FAIL: options.fail ?? '',
    ZTD_TEST_OS: options.os ?? 'Linux',
    ZTD_TEST_ARCH: options.arch ?? 'x86_64',
    ZTD_TEST_DIGEST: digest,
    ...options.env
  };
  return { work, scratch, log, env, digest };
}

function runInstaller(fixture, extraEnv = {}) {
  return spawnSync(SHELL, [installer], {
    cwd: fixture.work,
    env: { ...fixture.env, ...extraEnv },
    encoding: 'utf8'
  });
}

function assertNoLeftovers(fixture) {
  assert.deepEqual(readdirSync(fixture.scratch), [], 'temporary files must be removed');
}

// URLs the installer requested, in order. Empty when it never reached the network.
function requestedUrls(fixture) {
  const path = join(fixture.log, 'curl.log');
  return existsSync(path) ? readFileSync(path, 'utf8').split('\n').filter(Boolean) : [];
}

const hasSha256sum = Boolean(hostTool('sha256sum'));

test('installer installs ./ztd when the download and checksum match', { skip: !hasSha256sum }, (t) => {
  const fixture = setup(t, { sha256sum: true });
  const result = runInstaller(fixture);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Installing ztd 2\.3\.2/);
  assert.match(result.stdout, /Installed \.\/ztd/);
  assert.equal(readFileSync(join(fixture.work, 'ztd'), 'utf8'), '#!/bin/sh\necho ztd\n');
  assert.deepEqual(requestedUrls(fixture), [
    'https://zerotrustdata.io/latest.json',
    'https://zerotrustdata.io/v/2.3.2/ztd-linux-amd64',
    'https://zerotrustdata.io/v/2.3.2/ztd-linux-amd64.sha256',
    'https://zerotrustdata.io/v/2.3.2/ztd-linux-amd64.sigstore.json'
  ]);
  assert.match(result.stdout, /signature not checked/);
  assertNoLeftovers(fixture);
});

test('ZTD_REQUIRE_SIGSTORE=1 fails closed when cosign is not installed', (t) => {
  const fixture = setup(t, { shasum: true, sha256sum: hasSha256sum });
  const result = runInstaller(fixture, { ZTD_REQUIRE_SIGSTORE: '1' });

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /cosign is not installed/);
  assert.equal(existsSync(join(fixture.work, 'ztd')), false, 'a failed signature requirement must not leave a binary');
  assertNoLeftovers(fixture);
});

test('installer accepts a pretty-printed latest.json', (t) => {
  const fixture = setup(t, { shasum: true, latest: '{\n  "version": "2.3.2-rc.1"\n}\n' });
  const result = runInstaller(fixture);

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Installing ztd 2\.3\.2-rc\.1/);
  assert.match(requestedUrls(fixture)[1], /\/v\/2\.3\.2-rc\.1\/ztd-linux-amd64$/);
});

const MALFORMED_POINTERS = {
  notJson: '{not json',
  empty: '',
  wrongKey: '{"release":"2.3.2"}',
  nonVersion: '{"version":"latest"}',
  pathTraversal: '{"version":"2.3.2/../../x"}',
  leadingV: '{"version":"v2.3.2"}',
  trailingNewline: '{"version":"2.3.2\\n-x"}'
};

for (const [name, latest] of Object.entries(MALFORMED_POINTERS)) {
  test(`installer fails closed on a malformed latest.json (${name}) and installs nothing`, (t) => {
    const fixture = setup(t, { shasum: true, latest });
    const result = runInstaller(fixture);

    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /latest\.json does not name a valid release version/);
    assert.deepEqual(requestedUrls(fixture), ['https://zerotrustdata.io/latest.json'], 'no binary or checksum may be requested');
    assert.equal(existsSync(join(fixture.work, 'ztd')), false);
    assertNoLeftovers(fixture);
  });
}

test('installer fails before any binary download when latest.json is missing', (t) => {
  const fixture = setup(t, { shasum: true, latest: null });
  const result = runInstaller(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /download failed: https:\/\/zerotrustdata\.io\/latest\.json/);
  assert.deepEqual(requestedUrls(fixture), ['https://zerotrustdata.io/latest.json']);
  assert.equal(existsSync(join(fixture.work, 'ztd')), false);
  assertNoLeftovers(fixture);
});

test('ZTD_VERSION pins the version and skips latest.json', (t) => {
  const fixture = setup(t, { shasum: true, latest: null });
  const result = runInstaller(fixture, { ZTD_VERSION: '2.4.0-rc.1' });

  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Installing ztd 2\.4\.0-rc\.1/);
  assert.deepEqual(requestedUrls(fixture), [
    'https://zerotrustdata.io/v/2.4.0-rc.1/ztd-linux-amd64',
    'https://zerotrustdata.io/v/2.4.0-rc.1/ztd-linux-amd64.sha256',
    'https://zerotrustdata.io/v/2.4.0-rc.1/ztd-linux-amd64.sigstore.json'
  ]);
  assert.equal(readFileSync(join(fixture.work, 'ztd'), 'utf8'), '#!/bin/sh\necho ztd\n');
  assertNoLeftovers(fixture);
});

for (const version of ['latest', '2.3', 'v2.3.2', '2.3.2/../../x', '2.3.2 ', '2.3.2\n']) {
  test(`installer rejects an invalid ZTD_VERSION (${JSON.stringify(version)}) before any download`, (t) => {
    const fixture = setup(t, { shasum: true });
    const result = runInstaller(fixture, { ZTD_VERSION: version });

    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /invalid ZTD_VERSION/);
    assert.deepEqual(requestedUrls(fixture), []);
    assert.equal(existsSync(join(fixture.work, 'ztd')), false);
    assertNoLeftovers(fixture);
  });
}

test('installer falls back to shasum -a 256 when sha256sum is absent', (t) => {
  const fixture = setup(t, { shasum: true, sha256sum: false });
  const result = runInstaller(fixture);

  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(join(fixture.work, 'ztd'), 'utf8'), '#!/bin/sh\necho ztd\n');
  assert.match(readFileSync(join(fixture.log, 'shasum.log'), 'utf8'), /^shasum -a 256 /m);
  assertNoLeftovers(fixture);
});

test('installer fails clearly when neither sha256sum nor shasum exists', (t) => {
  const fixture = setup(t, { shasum: false, sha256sum: false });
  const result = runInstaller(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /no SHA-256 tool found/);
  assert.equal(existsSync(join(fixture.work, 'ztd')), false);
});

test('installer rejects a checksum mismatch and installs nothing', (t) => {
  const fixture = setup(t, {
    shasum: true,
    checksum: `${sha256Hex('tampered binary')}  ztd-linux-amd64\n`
  });
  const result = runInstaller(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /checksum verification failed/);
  assert.equal(existsSync(join(fixture.work, 'ztd')), false);
  assertNoLeftovers(fixture);
});

const MALFORMED_CHECKSUMS = {
  empty: '',
  whitespaceOnly: '   \n\n',
  notHex: `${'z'.repeat(64)}  ztd-linux-amd64\n`,
  tooShort: 'abcdef  ztd-linux-amd64\n',
  tooLong: `${'a'.repeat(65)}  ztd-linux-amd64\n`,
  multipleLines: 'a'.repeat(64) + '  one\n' + 'b'.repeat(64) + '  two\n',
  htmlErrorPage: '<html><body>Not Found</body></html>\n'
};

for (const [name, checksum] of Object.entries(MALFORMED_CHECKSUMS)) {
  test(`installer rejects a malformed checksum file (${name})`, (t) => {
    const fixture = setup(t, { shasum: true, checksum });
    const result = runInstaller(fixture);

    assert.notEqual(result.status, 0);
    assert.match(result.stderr, /malformed checksum file/);
    assert.equal(existsSync(join(fixture.work, 'ztd')), false);
    assertNoLeftovers(fixture);
  });
}

test('installer fails when the binary download fails (curl -f)', (t) => {
  const fixture = setup(t, { shasum: true, fail: 'binary' });
  const result = runInstaller(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /download failed: https:\/\/zerotrustdata\.io\/v\/2\.3\.2\/ztd-linux-amd64/);
  assert.equal(existsSync(join(fixture.work, 'ztd')), false);
  assertNoLeftovers(fixture);
});

test('installer fails when the checksum download fails (curl -f)', (t) => {
  const fixture = setup(t, { shasum: true, fail: 'checksum' });
  const result = runInstaller(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /download failed: .*\.sha256/);
  assert.equal(existsSync(join(fixture.work, 'ztd')), false);
  assertNoLeftovers(fixture);
});

test('installer rejects an empty binary download', (t) => {
  const fixture = setup(t, { shasum: true, binary: Buffer.alloc(0), checksum: `${sha256Hex('')}  ztd-linux-amd64\n` });
  const result = runInstaller(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /is empty/);
  assert.equal(existsSync(join(fixture.work, 'ztd')), false);
  assertNoLeftovers(fixture);
});

test('installer rejects unsupported operating systems before any download', (t) => {
  const fixture = setup(t, { shasum: true, os: 'SunOS' });
  const result = runInstaller(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /unsupported OS: sunos/);
  assert.equal(existsSync(join(fixture.log, 'curl.log')), false);
});

test('installer rejects unsupported architectures before any download', (t) => {
  const fixture = setup(t, { shasum: true, arch: 'riscv64' });
  const result = runInstaller(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /unsupported architecture: riscv64/);
  assert.equal(existsSync(join(fixture.log, 'curl.log')), false);
});

test('installer rejects combinations that have no published build', (t) => {
  const fixture = setup(t, { shasum: true, os: 'Darwin', arch: 'i686' });
  const result = runInstaller(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /no published ztd build for darwin\/386/);
  assert.equal(existsSync(join(fixture.log, 'curl.log')), false);
});

test('installer maps aarch64 to the arm64 artifact', (t) => {
  const fixture = setup(t, { shasum: true, arch: 'aarch64' });
  const result = runInstaller(fixture);

  assert.equal(result.status, 0, result.stderr);
  assert.match(readFileSync(join(fixture.log, 'curl.log'), 'utf8'), /ztd-linux-arm64/);
});

test('installer does not overwrite an existing ./ztd unless ZTD_OVERWRITE=1', (t) => {
  const fixture = setup(t, { shasum: true });
  writeFileSync(join(fixture.work, 'ztd'), 'operator-owned file\n');

  const refused = runInstaller(fixture);
  assert.notEqual(refused.status, 0);
  assert.match(refused.stderr, /already exists; set ZTD_OVERWRITE=1/);
  assert.equal(readFileSync(join(fixture.work, 'ztd'), 'utf8'), 'operator-owned file\n');
  assert.equal(existsSync(join(fixture.log, 'curl.log')), false, 'refusal must happen before any download');

  const replaced = runInstaller(fixture, { ZTD_OVERWRITE: '1' });
  assert.equal(replaced.status, 0, replaced.stderr);
  assert.equal(readFileSync(join(fixture.work, 'ztd'), 'utf8'), '#!/bin/sh\necho ztd\n');
});

test('installer works when the working directory and TMPDIR contain spaces', (t) => {
  const fixture = setup(t, { shasum: true });
  assert.match(fixture.work, / /);
  assert.match(fixture.scratch, / /);
  const result = runInstaller(fixture);

  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(join(fixture.work, 'ztd'), 'utf8'), '#!/bin/sh\necho ztd\n');
  assertNoLeftovers(fixture);
});

test('installer removes temporary files when terminated by SIGTERM mid-download', (t) => {
  const fixture = setup(t, { shasum: true });
  // Replace the curl stub: it signals the installer shell (its parent) as a kill would, then blocks.
  const curl = join(fixture.env.PATH, 'curl');
  writeFileSync(curl, `#!/bin/sh
kill -TERM "$PPID"
`);
  chmodSync(curl, 0o755);
  const result = runInstaller(fixture);

  assert.equal(result.status, 143, result.stderr);
  assert.equal(existsSync(join(fixture.work, 'ztd')), false);
  assertNoLeftovers(fixture);
});

test('installer fails when no temporary directory can be created, before any download', (t) => {
  const fixture = setup(t, { shasum: true, env: { TMPDIR: join(tmpdir(), 'ztd-missing-' + process.pid, 'nope') } });
  const result = runInstaller(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /could not create a temporary directory/);
  assert.equal(existsSync(join(fixture.log, 'curl.log')), false);
  assert.equal(existsSync(join(fixture.work, 'ztd')), false);
});

test('installer removes temporary files when interrupted by SIGINT mid-download', (t) => {
  const fixture = setup(t, { shasum: true });
  const curl = join(fixture.env.PATH, 'curl');
  writeFileSync(curl, `#!/bin/sh
kill -INT "$PPID"
`);
  chmodSync(curl, 0o755);
  const result = runInstaller(fixture);

  assert.equal(result.status, 130, result.stderr);
  assert.equal(existsSync(join(fixture.work, 'ztd')), false);
  assertNoLeftovers(fixture);
});

test('installer refuses to write through a symbolic link, even with ZTD_OVERWRITE=1', (t) => {
  const fixture = setup(t, { shasum: true });
  const outside = join(fixture.work, '..', 'outside-target');
  try {
    symlinkSync(outside, join(fixture.work, 'ztd'));
  } catch (err) {
    if (process.platform === 'win32' && err.code === 'EPERM') {
      t.skip('symlinks require Developer Mode on Windows');
      return;
    }
    throw err;
  }

  const result = runInstaller(fixture, { ZTD_OVERWRITE: '1' });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /is a symbolic link/);
  assert.equal(existsSync(outside), false, 'a dangling link must not be followed');
  assert.equal(existsSync(join(fixture.log, 'curl.log')), false, 'refusal must happen before any download');
});

test('installer refuses to replace a directory named ztd, even with ZTD_OVERWRITE=1', (t) => {
  const fixture = setup(t, { shasum: true });
  mkdirSync(join(fixture.work, 'ztd'));

  const result = runInstaller(fixture, { ZTD_OVERWRITE: '1' });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /is a directory; refusing to replace it/);
  assert.equal(existsSync(join(fixture.log, 'curl.log')), false);
});

test('installer announces when it replaces an existing ./ztd with ZTD_OVERWRITE=1', (t) => {
  const fixture = setup(t, { shasum: true });
  writeFileSync(join(fixture.work, 'ztd'), 'older file\n');

  const result = runInstaller(fixture, { ZTD_OVERWRITE: '1' });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stderr, /replacing existing \.\/ztd \(ZTD_OVERWRITE=1\); it was not checked as a previous ztd install/);
  assert.equal(readFileSync(join(fixture.work, 'ztd'), 'utf8'), '#!/bin/sh\necho ztd\n');
});

test('installer does not announce a replacement on a fresh install', (t) => {
  const fixture = setup(t, { shasum: true });
  const result = runInstaller(fixture);

  assert.equal(result.status, 0, result.stderr);
  assert.doesNotMatch(result.stderr, /replacing existing/);
});

test('installer installs into ZTD_INSTALL_DIR when it contains spaces, without sudo', (t) => {
  const fixture = setup(t, { shasum: true });
  const installDir = join(fixture.work, 'user bin dir');
  mkdirSync(installDir);

  const result = runInstaller(fixture, { ZTD_INSTALL_DIR: installDir });
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /Installed .*user bin dir\/ztd/);
  assert.equal(readFileSync(join(installDir, 'ztd'), 'utf8'), '#!/bin/sh\necho ztd\n');
  assert.equal(existsSync(join(fixture.work, 'ztd')), false, 'the working directory must not be used');
  assert.equal(existsSync(join(fixture.log, 'sudo.log')), false, 'sudo must never be invoked');
  assertNoLeftovers(fixture);
});

test('installer fails before any download when ZTD_INSTALL_DIR does not exist', (t) => {
  const fixture = setup(t, { shasum: true });
  const result = runInstaller(fixture, { ZTD_INSTALL_DIR: join(fixture.work, 'missing') });

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /install directory not found/);
  assert.equal(existsSync(join(fixture.log, 'curl.log')), false);
});

test('installer never invokes sudo when the install directory is not writable', (t) => {
  if (process.platform === 'win32') {
    t.skip('chmod does not enforce read-only directories on Windows');
    return;
  }
  if (process.getuid && process.getuid() === 0) {
    t.skip('root can write any directory');
    return;
  }
  const fixture = setup(t, { shasum: true });
  const installDir = join(fixture.work, 'readonly');
  mkdirSync(installDir);
  chmodSync(installDir, 0o555);
  let result;
  try {
    result = runInstaller(fixture, { ZTD_INSTALL_DIR: installDir });
  } finally {
    chmodSync(installDir, 0o755);
  }
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /install directory is not writable.*no sudo is attempted/);
  assert.equal(existsSync(join(fixture.log, 'sudo.log')), false, 'sudo must never be invoked');
  assert.equal(existsSync(join(fixture.log, 'curl.log')), false);
});

test('installer points Windows shells at ztd.ps1', (t) => {
  const fixture = setup(t, { shasum: true, os: 'MINGW64_NT-10.0-19045' });
  const result = runInstaller(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /unsupported OS: mingw64_nt-10\.0-19045; on Windows run ztd\.ps1/);
  assert.equal(existsSync(join(fixture.log, 'curl.log')), false);
});

test('installer script is POSIX sh with no bashisms in its shebang', () => {
  const source = readFileSync(installer, 'utf8');
  assert.equal(source.split('\n')[0], '#!/bin/sh');
  assert.doesNotMatch(source, /\[\[|\bfunction\s|\bpipefail\b/);
});
