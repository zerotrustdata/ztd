// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Offline tests for static/start.sh, the one-command onboarding script. The script runs exactly as users run it,
// piped into a shell on stdin, against a stub curl that serves the real static/ztd.sh installer, a release
// pointer, a fake ztd binary, and its checksum. The fake ztd records every invocation, every byte it receives on
// stdin, and models the Gateway and CLI identity as files, so tests can assert the exact command sequence.

import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { chmodSync, copyFileSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { delimiter, join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const startScript = fileURLToPath(new URL('../static/start.sh', import.meta.url));
const installScript = fileURLToPath(new URL('../static/ztd.sh', import.meta.url));
const HOST_PATH = (process.env.PATH || '').split(delimiter);
// Host tools start.sh, ztd.sh, and the fake ztd call. Everything else is hidden from the script PATH.
const REAL_TOOLS = ['sh', 'awk', 'sed', 'grep', 'tail', 'head', 'tr', 'mktemp', 'install', 'rm', 'cat', 'date', 'mkdir', 'wc', 'sha256sum', 'touch'];

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

function linkOrCopy(src, dst) {
  if (process.platform === 'win32') {
    writeFileSync(dst, `#!/bin/sh\nexec "${src.replace(/\\/g, '/')}" "$@"\n`);
    chmodSync(dst, 0o755);
  } else {
    symlinkSync(src, dst);
  }
}
const defaultShell = hostTool('sh') ? [hostTool('sh')] : ['/bin/sh'];

// Every POSIX shell available on this host; the script must behave identically under each.
const SHELLS = [
  ['sh', defaultShell],
  ['dash', hostTool('dash') && [hostTool('dash')]],
  ['busybox sh', hostTool('busybox') && [hostTool('busybox'), 'sh']],
  ['bash --posix', hostTool('bash') && [hostTool('bash'), '--posix']]
].filter(([, command]) => command);

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
  */ztd.sh) kind=installer ;;
  */latest.json) kind=latest ;;
  *.sha256) kind=checksum ;;
  *) kind=binary ;;
esac
[ "$ZTD_TEST_FAIL" = "$kind" ] && exit 22
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

const STUB_SUDO = `#!/bin/sh
echo "sudo $*" >> "$ZTD_TEST_LOG/sudo.log"
exit 1
`;

// The served "release binary". The Gateway and the CLI identity are files under ZTD_FAKE_STATE.
const FAKE_ZTD = `#!/bin/sh
state="$ZTD_FAKE_STATE"
echo "$*" >> "$state/calls.log"
received=$(cat | wc -c)
[ "$received" -eq 0 ] || echo "$* <- $received bytes" >> "$state/stdin.log"
case "$1 \${2:-}" in
  "--version ") echo "ztd version v9.9.9" ;;
  "gw status")
    if [ -f "$state/gateway" ]; then
      printf 'Gateway  online (PID 42)\\n\\nConsole  https://localhost:8443/console/\\n'
    else
      echo "Gateway  stopped"
    fi ;;
  "gw start")
    if [ -n "\${ZTD_FAKE_START_ERROR:-}" ]; then echo "Error: $ZTD_FAKE_START_ERROR" >&2; exit 1; fi
    touch "$state/gateway"; echo "[ztd] Gateway started" ;;
  "auth context") [ -f "$state/identity" ] ;;
  "login "*)
    [ -z "\${ZTD_FAKE_LOGIN_ERROR:-}" ] || { echo "Error: $ZTD_FAKE_LOGIN_ERROR" >&2; exit 1; }
    [ -f "$state/gateway" ] || exit 1
    touch "$state/identity"; echo "Signed in" ;;
  "operator run")
    [ -z "\${ZTD_FAKE_RUN_ERROR:-}" ] || { echo "Error: $ZTD_FAKE_RUN_ERROR" >&2; exit 1; }
    [ -f "$state/identity" ] || exit 1
    printf 'Operator session sess-123 (operator embedded-operator)\\n  stdout:\\n    hello from ztd\\n\\n' ;;
  *) echo "fake ztd: unexpected command: $*" >&2; exit 3 ;;
esac
`;

const sha256Hex = content => createHash('sha256').update(content).digest('hex');

function setup(t, options = {}) {
  const root = mkdtempSync(join(tmpdir(), 'ztd-start-test-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const paths = {
    serve: join(root, 'serve'),
    log: join(root, 'log'),
    state: join(root, 'state'),
    scratch: join(root, 'tmp with spaces'),
    home: join(root, 'home'),
    bin: join(root, 'bin')
  };
  Object.values(paths).forEach(path => mkdirSync(path, { recursive: true }));

  writeFileSync(join(paths.serve, 'installer'), readFileSync(installScript));
  writeFileSync(join(paths.serve, 'latest'), '{"version":"2.3.2"}\n');
  writeFileSync(join(paths.serve, 'binary'), FAKE_ZTD);
  writeFileSync(join(paths.serve, 'checksum'), options.checksum ?? `${sha256Hex(FAKE_ZTD)}  ztd-linux-amd64\n`);

  const writeStub = (name, body) => {
    writeFileSync(join(paths.bin, name), body);
    chmodSync(join(paths.bin, name), 0o755);
  };
  writeStub('curl', STUB_CURL);
  writeStub('uname', STUB_UNAME);
  writeStub('sudo', STUB_SUDO);
  for (const name of REAL_TOOLS) {
    const real = hostTool(name);
    if (real) linkOrCopy(real, join(paths.bin, name));
  }

  const env = {
    PATH: paths.bin,
    HOME: paths.home,
    TMPDIR: paths.scratch,
    ZTD_TEST_LOG: paths.log,
    ZTD_TEST_SERVE: paths.serve,
    ZTD_TEST_FAIL: options.fail ?? '',
    ZTD_TEST_OS: options.os ?? 'Linux',
    ZTD_TEST_ARCH: 'x86_64',
    ZTD_FAKE_STATE: paths.state,
    ...options.env
  };
  return { root, ...paths, env };
}

// Runs the script the way users do: `curl -fsSL https://zerotrustdata.io/start.sh | sh`. By the time main runs, the
// shell has normally read the whole script, so trailing comment lines stand in for stream bytes still in the pipe:
// any command that reads stdin would consume them.
const PIPE_TAIL = `\n${'# still streaming\n'.repeat(8192)}`;

function runPiped(fixture, { shell = defaultShell, env = {}, script = readFileSync(startScript, 'utf8') + PIPE_TAIL } = {}) {
  const [command, ...args] = shell;
  return spawnSync(command, args, { cwd: fixture.root, env: { ...fixture.env, ...env }, input: script, encoding: 'utf8' });
}

const readLines = path => existsSync(path) ? readFileSync(path, 'utf8').split('\n').filter(Boolean) : [];
const ztdCalls = fixture => readLines(join(fixture.state, 'calls.log'));
const curlUrls = fixture => readLines(join(fixture.log, 'curl.log'));
const ACTION = 'operator run --all-active --cmd echo hello from ztd';
const FRESH_CALLS = ['--version', 'gw status', 'gw start', 'auth context', 'login', ACTION, 'gw status'];

function assertClean(fixture) {
  assert.deepEqual(readdirSync(fixture.scratch), [], 'temporary files must be removed');
  assert.deepEqual(readLines(join(fixture.state, 'stdin.log')), [], 'no ztd command may read the piped script from stdin');
  assert.equal(existsSync(join(fixture.log, 'sudo.log')), false, 'sudo must never be invoked');
}

for (const [name, shell] of SHELLS) {
  test(`start.sh piped into ${name} installs, starts the Gateway, signs in, and runs a governed action`, (t) => {
    const fixture = setup(t);
    const result = runPiped(fixture, { shell });

    assert.equal(result.status, 0, result.stderr);
    const dir = join(fixture.home, 'ztd');
    assert.equal(readFileSync(join(dir, 'ztd'), 'utf8'), FAKE_ZTD);
    assert.deepEqual(curlUrls(fixture), [
      'https://zerotrustdata.io/ztd.sh',
      'https://zerotrustdata.io/latest.json',
      'https://zerotrustdata.io/v/2.3.2/ztd-linux-amd64',
      'https://zerotrustdata.io/v/2.3.2/ztd-linux-amd64.sha256'
    ]);
    assert.deepEqual(ztdCalls(fixture), FRESH_CALLS);
    assert.match(result.stdout, /Installed .*\/home\/ztd\/ztd — SHA-256 verified/);
    assert.match(result.stdout, /ztd version v9\.9\.9/);
    assert.match(result.stdout, /hello from ztd/);
    assert.match(result.stdout, /ztd is running/);
    assert.match(result.stdout, /cd ~\/ztd\n/);
    assert.match(result.stdout, /--operator-session-id sess-123/);
    assert.doesNotMatch(result.stdout, /\x1b\[/, 'no color codes when stdout is not a terminal');
    assertClean(fixture);
  });
}

test('rerunning start.sh reuses the binary, the running Gateway, and the CLI identity', (t) => {
  const fixture = setup(t);
  assert.equal(runPiped(fixture).status, 0);
  rmSync(join(fixture.state, 'calls.log'));
  rmSync(join(fixture.log, 'curl.log'));

  const result = runPiped(fixture);
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(curlUrls(fixture), [], 'a rerun downloads nothing');
  assert.deepEqual(ztdCalls(fixture), ['--version', 'gw status', 'auth context', ACTION, 'gw status']);
  assert.match(result.stdout, /reusing ~\/ztd\/ztd \(rerun with ZTD_OVERWRITE=1 to upgrade\)/);
  assert.match(result.stdout, /already running — reusing it/);
  assert.match(result.stdout, /CLI already signed in — reusing the identity/);
  assertClean(fixture);
});

test('ZTD_OVERWRITE=1 replaces an existing binary through the verified installer', (t) => {
  const fixture = setup(t);
  const dir = join(fixture.home, 'ztd');
  mkdirSync(dir);
  writeFileSync(join(dir, 'ztd'), FAKE_ZTD.replace('v9.9.9', 'v0.0.1'));
  chmodSync(join(dir, 'ztd'), 0o755);

  const result = runPiped(fixture, { env: { ZTD_OVERWRITE: '1' } });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(join(dir, 'ztd'), 'utf8'), FAKE_ZTD);
  assert.match(result.stdout, /ztd version v9\.9\.9/);
});

test('ZTD_VERSION pins the release that the installer downloads', (t) => {
  const fixture = setup(t);
  const result = runPiped(fixture, { env: { ZTD_VERSION: '2.4.0-rc.1' } });

  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(curlUrls(fixture), [
    'https://zerotrustdata.io/ztd.sh',
    'https://zerotrustdata.io/v/2.4.0-rc.1/ztd-linux-amd64',
    'https://zerotrustdata.io/v/2.4.0-rc.1/ztd-linux-amd64.sha256'
  ]);
});

function toPosixPath(p) {
  if (process.platform === 'win32') {
    return p.replace(/^([a-zA-Z]):\\/, (_, drive) => `/${drive.toLowerCase()}/`).replace(/\\/g, '/');
  }
  return p;
}

test('ZTD_DIR chooses the install directory and a path with spaces is quoted in the cd hint', (t) => {
  const fixture = setup(t);
  const dir = join(fixture.root, 'my ztd');
  const result = runPiped(fixture, { env: { ZTD_DIR: dir } });

  assert.equal(result.status, 0, result.stderr);
  assert.equal(readFileSync(join(dir, 'ztd'), 'utf8'), FAKE_ZTD);
  assert.equal(existsSync(join(fixture.home, 'ztd')), false);
  assert.ok(result.stdout.includes(`cd '${dir}'`) || result.stdout.includes(`cd '${toPosixPath(dir)}'`) || /cd '.*my ztd'/.test(result.stdout), result.stdout);
});

test('ZTD_MASTER_KEY_FILE is passed to every ztd command that takes it', (t) => {
  const fixture = setup(t);
  const key = join(fixture.root, 'master.key');
  writeFileSync(key, 'a2V5\n', { mode: 0o600 });
  const posixKey = toPosixPath(key);
  const result = runPiped(fixture, { env: { ZTD_MASTER_KEY_FILE: posixKey } });

  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(ztdCalls(fixture), FRESH_CALLS.map(call => call === '--version' ? call : `${call} --master-key-file ${posixKey}`));
  assert.ok(result.stdout.includes(`./ztd tui --master-key-file ${posixKey}`), 'next steps carry the key file');
});

for (const [name, value, message] of [
  ['relative', 'master.key', /must be an absolute path/],
  ['missing', '/nonexistent/ztd-master.key', /ZTD_MASTER_KEY_FILE not found/]
]) {
  test(`a ${name} ZTD_MASTER_KEY_FILE fails before any download or directory change`, (t) => {
    const fixture = setup(t);
    const result = runPiped(fixture, { env: { ZTD_MASTER_KEY_FILE: value } });

    assert.notEqual(result.status, 0);
    assert.match(result.stderr, message);
    assert.deepEqual(curlUrls(fixture), []);
    assert.equal(existsSync(join(fixture.home, 'ztd')), false);
  });
}

test('a Gateway with no usable key store explains the ssh-agent and key file remedies and stops', (t) => {
  const fixture = setup(t, { env: { ZTD_FAKE_START_ERROR: 'an OS-protected master key store is required but none is available' } });
  const result = runPiped(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /OS-protected master key store is required/, 'the Gateway error is shown');
  assert.match(result.stderr, /No usable OS key store/);
  assert.match(result.stderr, /ssh-agent/);
  assert.match(result.stderr, /ZTD_MASTER_KEY_FILE="\$HOME\/\.ztd-master\.key" sh/);
  assert.match(result.stderr, /the Gateway did not start/);
  assert.deepEqual(ztdCalls(fixture), ['--version', 'gw status', 'gw start'], 'no login after a failed start');
  assertClean(fixture);
});

test('a Gateway port conflict points at the Gateway already running elsewhere', (t) => {
  const fixture = setup(t, { env: { ZTD_FAKE_START_ERROR: 'listen tcp :8081: bind: address already in use' } });
  const result = runPiped(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /A port the Gateway needs is in use/);
  assert.match(result.stderr, /\.\/ztd gw stop/);
});

test('a failed login shows its output and stops before the governed action', (t) => {
  const fixture = setup(t, { env: { ZTD_FAKE_LOGIN_ERROR: 'enrollment refused' } });
  const result = runPiped(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /enrollment refused/);
  assert.match(result.stderr, /ztd login failed/);
  assert.equal(ztdCalls(fixture).some(call => call.startsWith('operator run')), false);
});

test('a failed governed action shows its output and exits non-zero', (t) => {
  const fixture = setup(t, { env: { ZTD_FAKE_RUN_ERROR: 'denied at L1' } });
  const result = runPiped(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /denied at L1/);
  assert.match(result.stderr, /the first governed action failed/);
  assert.doesNotMatch(result.stdout, /ztd is running/);
});

test('a checksum mismatch installs nothing and runs no ztd command', (t) => {
  const fixture = setup(t, { checksum: `${sha256Hex('tampered')}  ztd-linux-amd64\n` });
  const result = runPiped(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /checksum verification failed/);
  assert.match(result.stderr, /installing the ztd binary failed; nothing else was changed/);
  assert.equal(existsSync(join(fixture.home, 'ztd', 'ztd')), false);
  assert.deepEqual(ztdCalls(fixture), []);
  assertClean(fixture);
});

test('a failed installer download stops with the URL', (t) => {
  const fixture = setup(t, { fail: 'installer' });
  const result = runPiped(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /download failed: https:\/\/zerotrustdata\.io\/ztd\.sh/);
  assert.deepEqual(ztdCalls(fixture), []);
});

test('Windows shells are pointed at start.ps1 before any download', (t) => {
  const fixture = setup(t, { os: 'MINGW64_NT-10.0-19045' });
  const result = runPiped(fixture);

  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /irm https:\/\/zerotrustdata\.io\/start\.ps1 \| iex/);
  assert.deepEqual(curlUrls(fixture), []);
});

// A dropped connection delivers a prefix of the script. Because all work happens in main, called on the last line,
// no prefix may download, create directories, or run anything.
for (const [label, path] of [['start.sh', startScript], ['ztd.sh', installScript]]) {
  test(`a truncated ${label} download has no side effects`, (t) => {
    const fixture = setup(t);
    const source = readFileSync(path, 'utf8');
    const finalCall = source.lastIndexOf('main "$@"');
    for (const cut of [0.25, 0.5, 0.75, 0.99].map(fraction => Math.floor(finalCall * fraction)).concat(finalCall)) {
      runPiped(fixture, { script: source.slice(0, cut) });
      assert.deepEqual(curlUrls(fixture), [], `prefix of ${cut} bytes made a request`);
      assert.deepEqual(readdirSync(fixture.home), [], `prefix of ${cut} bytes created files`);
      assert.deepEqual(ztdCalls(fixture), []);
    }
  });
}

for (const [label, path] of [['start.sh', startScript], ['ztd.sh', installScript]]) {
  test(`${label} is POSIX sh and ends by calling main`, () => {
    const source = readFileSync(path, 'utf8');
    assert.equal(source.split('\n')[0], '#!/bin/sh');
    assert.doesNotMatch(source, /\[\[ |\bfunction\s|\bpipefail\b|\blocal\s/);
    assert.equal(source.trimEnd().split('\n').at(-1), 'main "$@"');
  });

  test(`${label} passes shellcheck`, { skip: !hostTool('shellcheck') && 'shellcheck is not installed' }, () => {
    const result = spawnSync(hostTool('shellcheck'), ['--shell=sh', '--severity=style', path], { encoding: 'utf8' });
    assert.equal(result.status, 0, result.stdout + result.stderr);
  });
}
