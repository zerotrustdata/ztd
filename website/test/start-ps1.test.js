// Copyright (c) 2026 Lateralus Labs, LLC.
// Licensed under the Business Source License 1.1 — see LICENSE for details.

// Offline tests for static/start.ps1, the one-command Windows onboarding script. Each test runs exactly what users
// paste, `irm https://zerotrustdata.io/start.ps1 | iex`, in a real PowerShell session whose Invoke-RestMethod and
// Invoke-WebRequest are replaced by a stub release host serving start.ps1, the real static/ztd.ps1 installer, a
// release pointer, a fake ztd.exe, and its checksum. The fake ztd.exe records every invocation and models the
// Gateway and CLI identity as files, so tests can assert the exact command sequence. Skipped when pwsh is absent.

import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { delimiter, join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const startScript = fileURLToPath(new URL('../static/start.ps1', import.meta.url));
const installScript = fileURLToPath(new URL('../static/ztd.ps1', import.meta.url));

function findPwsh() {
  if (process.env.ZTD_TEST_PWSH) return process.env.ZTD_TEST_PWSH;
  for (const directory of (process.env.PATH || '').split(delimiter)) {
    for (const name of ['pwsh', 'pwsh.exe']) {
      try {
        if (statSync(join(directory, name)).isFile()) return join(directory, name);
      } catch {
        // not in this directory
      }
    }
  }
  return null;
}
const PWSH = findPwsh();
const skip = !PWSH && 'pwsh is not installed (set ZTD_TEST_PWSH to its path)';

// The served "release binary". On Windows hosts it would be a real executable; here it is a POSIX script that
// pwsh runs by path like any native command.
const FAKE_ZTD = `#!/bin/sh
state="$ZTD_FAKE_STATE"
echo "$*" >> "$state/calls.log"
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

// Stub release host. Function definitions shadow the cmdlets of the same name for every script in the session,
// including ztd.ps1 run by start.ps1. Copy-Item marks the installed binary executable, as Windows needs no mode bit.
const PRELUDE = `
$ErrorActionPreference = 'Stop'
function global:Get-ZtdTestKind($Uri) {
    Add-Content -LiteralPath (Join-Path $Env:ZTD_TEST_LOG 'requests.log') -Value $Uri
    $kind = switch -Regex ($Uri) { '/start\\.ps1$' { 'start' } '/ztd\\.ps1$' { 'installer' } '/latest\\.json$' { 'latest' } '\\.sha256$' { 'checksum' } default { 'binary' } }
    if ($Env:ZTD_TEST_FAIL -eq $kind) { throw "stub: 404 for $Uri" }
    Join-Path $Env:ZTD_TEST_SERVE $kind
}
function global:Invoke-RestMethod { param($Uri, [switch]$UseBasicParsing)
    $content = Get-Content -LiteralPath (Get-ZtdTestKind $Uri) -Raw
    if ($Uri -like '*.json') { $content | ConvertFrom-Json } else { $content }
}
function global:Invoke-WebRequest { param($Uri, $OutFile, [switch]$UseBasicParsing)
    Microsoft.PowerShell.Management\\Copy-Item -LiteralPath (Get-ZtdTestKind $Uri) -Destination $OutFile -Force
}
function global:Copy-Item {
    Microsoft.PowerShell.Management\\Copy-Item @args
    if (Test-Path ./ztd.exe) { [System.IO.File]::SetUnixFileMode((Resolve-Path ./ztd.exe).Path, 'UserRead, UserWrite, UserExecute') }
}
`;

const sha256Hex = content => createHash('sha256').update(content).digest('hex');

function setup(t, options = {}) {
  const root = mkdtempSync(join(tmpdir(), 'ztd-start-ps1-test-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  const paths = {
    serve: join(root, 'serve'),
    log: join(root, 'log'),
    state: join(root, 'state'),
    home: join(root, 'home'),
    cwd: join(root, 'caller')
  };
  Object.values(paths).forEach(path => mkdirSync(path, { recursive: true }));

  writeFileSync(join(paths.serve, 'start'), options.script ?? readFileSync(startScript));
  writeFileSync(join(paths.serve, 'installer'), readFileSync(installScript));
  writeFileSync(join(paths.serve, 'latest'), '{"version":"2.3.2"}\n');
  writeFileSync(join(paths.serve, 'binary'), FAKE_ZTD);
  writeFileSync(join(paths.serve, 'checksum'), options.checksum ?? `${sha256Hex(FAKE_ZTD)}  ztd-windows-amd64.exe\n`);

  const env = {
    PATH: process.env.PATH,
    HOME: paths.home,
    TMPDIR: process.env.TMPDIR || tmpdir(),
    PROCESSOR_ARCHITECTURE: 'AMD64',
    POWERSHELL_TELEMETRY_OPTOUT: '1',
    ZTD_TEST_LOG: paths.log,
    ZTD_TEST_SERVE: paths.serve,
    ZTD_TEST_FAIL: options.fail ?? '',
    ZTD_FAKE_STATE: paths.state,
    ...options.env
  };
  return { root, ...paths, env };
}

// Runs the pasted command in a fresh session, then reports whether the session survived and where it is, the
// way a user's prompt would come back after `irm | iex`.
function run(fixture, env = {}) {
  const command = `${PRELUDE}
Set-Location -LiteralPath $Env:ZTD_TEST_CWD
try { irm https://zerotrustdata.io/start.ps1 | iex } catch { Write-Output "FAILED: $_" }
Write-Output "SESSION-ALIVE $((Get-Location).Path)"`;
  const result = spawnSync(PWSH, ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command', command], {
    env: { ...fixture.env, ZTD_TEST_CWD: fixture.cwd, ...env },
    stdio: ['ignore', 'pipe', 'pipe'],
    encoding: 'utf8'
  });
  return { ...result, output: `${result.stdout}${result.stderr}` };
}

const readLines = path => existsSync(path) ? readFileSync(path, 'utf8').split('\n').filter(Boolean) : [];
const ztdCalls = fixture => readLines(join(fixture.state, 'calls.log'));
const requests = fixture => readLines(join(fixture.log, 'requests.log'));
const ACTION = 'operator run --all-active --cmd echo hello from ztd';
const FRESH_CALLS = ['--version', 'gw status', 'gw start', 'auth context', 'login', ACTION, 'gw status'];
const START = 'https://zerotrustdata.io/start.ps1';
const INSTALL_REQUESTS = [
  START,
  'https://zerotrustdata.io/ztd.ps1',
  'https://zerotrustdata.io/latest.json',
  'https://zerotrustdata.io/v/2.3.2/ztd-windows-amd64.exe',
  'https://zerotrustdata.io/v/2.3.2/ztd-windows-amd64.exe.sha256'
];

// The caller's session must survive and keep its location whether the script succeeds or fails.
function assertSessionIntact(fixture, result) {
  assert.equal(result.status, 0, result.output);
  assert.match(result.stdout, new RegExp(`SESSION-ALIVE ${fixture.cwd.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\\n`));
}

test('start.ps1 via irm | iex installs, starts the Gateway, signs in, and runs a governed action', { skip }, (t) => {
  const fixture = setup(t);
  const result = run(fixture);

  assertSessionIntact(fixture, result);
  assert.doesNotMatch(result.output, /FAILED/);
  const dir = join(fixture.home, 'ztd');
  assert.equal(readFileSync(join(dir, 'ztd.exe'), 'utf8'), FAKE_ZTD);
  assert.deepEqual(requests(fixture), INSTALL_REQUESTS);
  assert.deepEqual(ztdCalls(fixture), FRESH_CALLS);
  assert.match(result.stdout, /installed ztd\.exe - SHA-256 verified/);
  assert.doesNotMatch(result.stdout, /Installing ztd 2\.3\.2|Installed \.\\ztd\.exe/, "ztd.ps1's own progress lines are replaced");
  assert.match(result.stdout, /ztd version v9\.9\.9/);
  assert.match(result.stdout, /Gateway online/);
  assert.match(result.stdout, /hello from ztd/);
  assert.match(result.stdout, /ztd is running/);
  assert.ok(result.stdout.includes(`cd '${dir}'`));
  assert.match(result.stdout, /\.\\ztd\.exe login web +add a passkey, then open https:\/\/localhost:8443\/console\//);
  assert.match(result.stdout, /--operator-session-id sess-123/);
});

test('rerunning start.ps1 reuses the binary, the running Gateway, and the CLI identity', { skip }, (t) => {
  const fixture = setup(t);
  assert.doesNotMatch(run(fixture).output, /FAILED/);
  rmSync(join(fixture.state, 'calls.log'));
  rmSync(join(fixture.log, 'requests.log'));

  const result = run(fixture);
  assertSessionIntact(fixture, result);
  assert.doesNotMatch(result.output, /FAILED/);
  assert.deepEqual(requests(fixture), [START], 'a rerun downloads nothing but the script');
  assert.deepEqual(ztdCalls(fixture), ['--version', 'gw status', 'auth context', ACTION, 'gw status']);
  assert.match(result.stdout, /reusing .*ztd\.exe \(rerun with \$Env:ZTD_OVERWRITE='1' to upgrade\)/);
  assert.match(result.stdout, /already running - reusing it/);
  assert.match(result.stdout, /CLI already signed in - reusing the identity/);
});

test("ZTD_OVERWRITE=1 replaces an existing ztd.exe through the verified installer", { skip }, (t) => {
  const fixture = setup(t);
  const dir = join(fixture.home, 'ztd');
  mkdirSync(dir);
  writeFileSync(join(dir, 'ztd.exe'), 'old');

  const result = run(fixture, { ZTD_OVERWRITE: '1' });
  assertSessionIntact(fixture, result);
  assert.doesNotMatch(result.output, /FAILED/);
  assert.equal(readFileSync(join(dir, 'ztd.exe'), 'utf8'), FAKE_ZTD);
  assert.deepEqual(requests(fixture), INSTALL_REQUESTS);
});

test('ZTD_DIR and ZTD_VERSION choose the directory and release', { skip }, (t) => {
  const fixture = setup(t);
  const dir = join(fixture.root, "it's my ztd");
  const result = run(fixture, { ZTD_DIR: dir, ZTD_VERSION: '2.0.1' });

  assertSessionIntact(fixture, result);
  assert.doesNotMatch(result.output, /FAILED/);
  assert.equal(readFileSync(join(dir, 'ztd.exe'), 'utf8'), FAKE_ZTD);
  assert.equal(existsSync(join(fixture.home, 'ztd')), false);
  assert.deepEqual(requests(fixture), [
    START,
    'https://zerotrustdata.io/ztd.ps1',
    'https://zerotrustdata.io/v/2.0.1/ztd-windows-amd64.exe',
    'https://zerotrustdata.io/v/2.0.1/ztd-windows-amd64.exe.sha256'
  ]);
  assert.ok(result.stdout.includes(`cd '${dir.replace("'", "''")}'`), 'the cd hint quotes the path for PowerShell');
});

test('an unsupported architecture fails before any download', { skip }, (t) => {
  const fixture = setup(t);
  const result = run(fixture, { PROCESSOR_ARCHITECTURE: 'x86' });

  assertSessionIntact(fixture, result);
  assert.match(result.stdout, /FAILED: ztd start: unsupported architecture: x86/);
  assert.deepEqual(requests(fixture), [START]);
});

test('a checksum mismatch installs nothing and runs nothing', { skip }, (t) => {
  const fixture = setup(t, { checksum: `${'0'.repeat(64)}  ztd-windows-amd64.exe\n` });
  const result = run(fixture);

  assertSessionIntact(fixture, result);
  assert.match(result.stdout, /FAILED: ztd start: installing the ztd binary failed; nothing else was changed: Checksum verification failed/);
  assert.equal(existsSync(join(fixture.home, 'ztd', 'ztd.exe')), false);
  assert.deepEqual(ztdCalls(fixture), []);
});

test('a failed installer download is reported and runs nothing', { skip }, (t) => {
  const fixture = setup(t, { fail: 'installer' });
  const result = run(fixture);

  assertSessionIntact(fixture, result);
  assert.match(result.stdout, /FAILED: ztd start: installing the ztd binary failed/);
  assert.deepEqual(ztdCalls(fixture), []);
});

test('a busy Gateway port shows its remedy', { skip }, (t) => {
  const fixture = setup(t);
  const result = run(fixture, { ZTD_FAKE_START_ERROR: 'listen tcp 127.0.0.1:8443: bind: Only one usage of each socket address (protocol/network address/port) is normally permitted.' });

  assertSessionIntact(fixture, result);
  assert.match(result.output, /Only one usage of each socket address/, 'the failed step output is shown');
  assert.match(result.output, /A port the Gateway needs is in use/);
  assert.match(result.stdout, /FAILED: ztd start: the Gateway did not start/);
  assert.deepEqual(ztdCalls(fixture), ['--version', 'gw status', 'gw start']);
});

for (const [label, variable, message, calls] of [
  ['login', 'ZTD_FAKE_LOGIN_ERROR', /FAILED: ztd start: ztd login failed/, ['--version', 'gw status', 'gw start', 'auth context', 'login']],
  ['governed action', 'ZTD_FAKE_RUN_ERROR', /FAILED: ztd start: the first governed action failed/, FRESH_CALLS.slice(0, -1)]
]) {
  test(`a failed ${label} shows its output and stops`, { skip }, (t) => {
    const fixture = setup(t);
    const result = run(fixture, { [variable]: 'boom from fake ztd' });

    assertSessionIntact(fixture, result);
    assert.match(result.output, /Error: boom from fake ztd/);
    assert.match(result.stdout, message);
    assert.deepEqual(ztdCalls(fixture), calls);
  });
}

// A dropped connection delivers a prefix of the script. All work happens inside one script block, so any prefix
// fails to parse and runs nothing.
test('a truncated start.ps1 download has no side effects', { skip }, (t) => {
  const source = readFileSync(startScript, 'utf8');
  const blockStart = source.indexOf('& {');
  const end = source.trimEnd().length;
  for (const cut of [blockStart + 3, ...[0.25, 0.5, 0.75, 0.99].map(fraction => blockStart + Math.floor((end - blockStart) * fraction)), end - 1]) {
    const fixture = setup(t, { script: source.slice(0, cut) });
    const result = run(fixture);
    assertSessionIntact(fixture, result);
    assert.match(result.stdout, /FAILED: /, `prefix of ${cut} bytes did not fail to parse`);
    assert.deepEqual(requests(fixture), [START], `prefix of ${cut} bytes made a request`);
    assert.equal(existsSync(join(fixture.home, 'ztd')), false, `prefix of ${cut} bytes created the install directory`);
    assert.deepEqual(ztdCalls(fixture), []);
  }
});

test('start.ps1 is ASCII, parses cleanly, and keeps all work in one script block', { skip }, () => {
  const source = readFileSync(startScript, 'utf8');
  assert.doesNotMatch(source, /[^\x00-\x7f]/, 'Windows PowerShell 5.1 decodes a charset-less response as Latin-1');
  assert.doesNotMatch(source, /^\s*exit\b/m, 'exit would close the window of a user running irm | iex');
  // .gitattributes checks *.ps1 out with CRLF line endings, so split on either ending.
  const code = source.split(/\r?\n/).filter(line => line.trim() && !line.startsWith('#'));
  assert.equal(code[0], '& {');
  assert.equal(code.at(-1), '}');
  const result = spawnSync(PWSH, ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command',
    `$errors = $null; [void][System.Management.Automation.Language.Parser]::ParseFile('${startScript}', [ref]$null, [ref]$errors); $errors | ForEach-Object { $_.ToString() }; exit $errors.Count`
  ], { encoding: 'utf8' });
  assert.equal(result.status, 0, result.stdout + result.stderr);
});
