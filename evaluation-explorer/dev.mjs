// Local Vite supervisor for the evaluation explorer.
//
// Requires the gateway-owned public spectator (enabled by default on `ztd gw start`):
//   ./ztd gw start -f --public-spectator
//
// This script optionally seeds fixture data in mock mode, replays scripted live
// events, and runs Vite. It does not start a mirror process. It exits when Vite
// exits, with Vite's exit code, after stopping any other child it started.
//
// Usage:
//   node dev.mjs --mode mock   # seed + replay + frontend (gateway must be up)
//   node dev.mjs --mode real   # frontend only; eval launch remains explicit
//   node dev.mjs --mode mock --no-replay
//
// Environment:
//   ZTD_BIN          path to the ztd binary (auto-detected if unset)
//   ZTD_PUBLIC_PORT  public mirror port (default: 8082)
//   ZTD_VITE_PORT    Vite dev server port (default: 5173)

import { spawn, spawnSync } from 'node:child_process';
import { existsSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const projectRoot = __dirname;

const PUBLIC_PORT = process.env.ZTD_PUBLIC_PORT ?? '8082';
const VITE_PORT = process.env.ZTD_VITE_PORT ?? '5173';

function parseArgs() {
  const argv = process.argv.slice(2);
  let mode = 'mock';
  let replay = true;
  let reset = false;
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--mode' && i + 1 < argv.length) {
      const m = argv[++i];
      if (m !== 'mock' && m !== 'real') throw new Error(`invalid --mode: ${m}`);
      mode = m;
    } else if (argv[i] === '--no-replay') {
      replay = false;
    } else if (argv[i] === '--reset') {
      reset = true;
    }
  }
  return { mode, replay, reset };
}

function ztdBin() {
  const env = process.env.ZTD_BIN;
  if (env) return env;
  let dir = projectRoot;
  for (let i = 0; i < 5; i++) {
    const candidate = resolve(dir, 'ztd');
    if (existsSync(candidate)) return candidate;
    dir = resolve(dir, '..');
  }
  throw new Error('ztd binary not found; set ZTD_BIN to the full path');
}

function ztdCwd() {
  return resolve(ztdBin(), '..');
}

function checkPort(port) {
  const result = spawnSync('ss', ['-ltn', `sport = :${port}`], { encoding: 'utf8' });
  if (result.status !== 0) return false;
  return result.stdout.includes(`:${port}`);
}

const children = [];

function spawnChild(label, cmd, args, cwd) {
  const proc = spawn(cmd, args, { cwd, stdio: ['ignore', 'pipe', 'pipe'] });
  children.push({ proc, label });
  proc.stdout?.on('data', (data) => process.stdout.write(`[${label}] ${data}`));
  proc.stderr?.on('data', (data) => process.stderr.write(`[${label}] ${data}`));
  proc.on('exit', (code, signal) => {
    console.log(`[${label}] exited with ${code !== null ? `code ${code}` : `signal ${signal}`}`);
    // The supervisor exists to run this child: stop anything else it started and
    // exit with the child's status (non-zero when it failed or was killed).
    killAll();
    process.exit(code ?? 1);
  });
  return proc;
}

function killAll() {
  for (const { proc, label } of children) {
    if (proc.exitCode === null && proc.signalCode === null) {
      console.log(`Stopping ${label} (pid ${proc.pid})...`);
      proc.kill('SIGTERM');
    }
  }
}

process.on('SIGINT', () => {
  console.log('\nShutting down...');
  killAll();
  process.exit(0);
});
process.on('SIGTERM', () => {
  killAll();
  process.exit(0);
});
process.on('exit', () => {
  killAll();
});

async function main() {
  const { mode, replay, reset } = parseArgs();
  const bin = ztdBin();
  const cwd = ztdCwd();

  console.log('=== OpenDevOps.ai Local Dev Supervisor ===');
  console.log(`Mode: ${mode}`);
  console.log(`ztd:  ${bin}`);
  console.log(`cwd:  ${cwd}`);

  if (reset) {
    console.error('\n--reset is no longer supported by dev.mjs.');
    console.error('Reset gateway spectator state: stop the Gateway, remove .ztd/public-feed and .ztd/public-mirror, then run ./ztd gw start --public-spectator');
    process.exit(1);
  }

  if (!checkPort(PUBLIC_PORT)) {
    console.error(`\nPublic mirror is not listening on port ${PUBLIC_PORT}.`);
    console.error('Start the gateway with --public-spectator first:');
    console.error('  ./ztd gw start -f --public-spectator');
    process.exit(1);
  }
  console.log(`\nGateway-owned mirror detected on port ${PUBLIC_PORT}.`);

  if (mode === 'mock') {
    console.log('\n--- Seeding fixture data ---');
    const seedResult = spawnSync('node', [resolve(__dirname, 'scripts/seed.mjs'), '--fixtures'], {
      cwd: projectRoot,
      encoding: 'utf8',
      stdio: 'inherit',
      env: { ...process.env, ZTD_BIN: bin, ZTD_CWD: cwd },
    });
    if (seedResult.status !== 0) {
      console.error('Fixture seed failed. Continuing with whatever data is available.');
    }
  }

  if (mode === 'mock' && replay) {
    console.log('\n--- Replaying scripted live events ---');
    const replayResult = spawnSync('node', [resolve(__dirname, 'scripts/replay.mjs'), '--interval', '800'], {
      cwd: projectRoot,
      encoding: 'utf8',
      stdio: 'inherit',
      env: { ...process.env, ZTD_BIN: bin, ZTD_CWD: cwd },
    });
    if (replayResult.status !== 0) {
      console.error('Replay failed. Frontend will still start.');
    }
  }

  console.log('\n--- Mirror health ---');
  spawnSync('node', [resolve(__dirname, 'scripts/mirror-health.mjs')], {
    cwd: projectRoot,
    encoding: 'utf8',
    stdio: 'inherit',
    env: { ...process.env, ZTD_BIN: bin, ZTD_CWD: cwd },
  });

  console.log('\n--- Starting Vite dev server ---');
  const viteBin = resolve(projectRoot, 'node_modules/.bin/vite');
  if (!existsSync(viteBin)) {
    console.error('Vite not found. Run npm install in the frontend project first.');
    killAll();
    process.exit(1);
  }
  spawnChild('vite', viteBin, [
    '--host', '127.0.0.1',
    '--port', VITE_PORT,
    '--strictPort',
  ], projectRoot);

  console.log(`\n=== Dev environment ready ===`);
  console.log(`Frontend:   http://127.0.0.1:${VITE_PORT}`);
  console.log(`Mirror:     http://127.0.0.1:${PUBLIC_PORT} (gateway-owned, read-only)`);
  console.log(`\nPress Ctrl+C to stop Vite.`);

  await new Promise(() => {});
}

main().catch((err) => {
  console.error('Supervisor failed:', err);
  killAll();
  process.exit(1);
});
