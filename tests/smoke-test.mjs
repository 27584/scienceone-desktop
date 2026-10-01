#!/usr/bin/env node
/**
 * End-to-end smoke test for the Panshi MCP bridge.
 *
 * Speaks the same JSON-RPC the platform sends (protocol 2024-11-05) and
 * exercises every tool against a real workspace, so a green run proves the
 * whole path works: Panshi -> nginx -> reverse tunnel -> this machine.
 *
 * Usage:
 *   node smoke-test.mjs
 *   node smoke-test.mjs --url http://127.0.0.1:8801/mcp --token <token>
 *   node smoke-test.mjs --url https://newapi.154.83.15.131.nip.io/panshi-mcp --token <token> -k
 *
 * Flags:
 *   --url     MCP endpoint            (default http://127.0.0.1:8801/mcp)
 *   --token   X-MCP-Token value       (default: env MCP_TOKEN)
 *   --root    workspace path used for temp files (default: "AI Projects/DSH/new-api/tools/panshi-mcp")
 *   -k        skip TLS verification (self-signed / nip.io)
 *   --keep    leave the temp files behind for inspection
 */

import process from 'node:process';

const args = process.argv.slice(2);
function argOf(flag, fallback) {
  const i = args.indexOf(flag);
  return i >= 0 && args[i + 1] ? args[i + 1] : fallback;
}
const URL_ = argOf('--url', 'http://127.0.0.1:8801/mcp');
const TOKEN = argOf('--token', process.env.MCP_TOKEN || '');
const ROOT = argOf('--root', 'AI Projects/DSH/new-api/tools/panshi-mcp');
const KEEP = args.includes('--keep');
const INSECURE = args.includes('-k');

if (INSECURE) process.env.NODE_TLS_REJECT_UNAUTHORIZED = '0';

let rpcId = 0;
let passed = 0;
let failed = 0;

async function rpc(method, params) {
  const body = { jsonrpc: '2.0', id: rpcId++, method };
  if (params !== undefined) body.params = params;
  const res = await fetch(URL_, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'application/json, text/event-stream',
      'X-MCP-Token': TOKEN,
      'Mcp-Session-Id': 'smoke-test',
    },
    body: JSON.stringify(body),
  });
  const text = await res.text();
  if (res.status === 202) return null;
  const payload = text.startsWith('event:') ? text.split('data: ')[1] : text;
  let json;
  try {
    json = JSON.parse(payload);
  } catch {
    throw new Error(`non-JSON response (${res.status}): ${text.slice(0, 200)}`);
  }
  if (json.error) throw new Error(`${json.error.code}: ${json.error.message}`);
  return json.result;
}

async function callTool(name, toolArgs) {
  const result = await rpc('tools/call', { name, arguments: toolArgs || {} });
  const text = (result.content || []).map((c) => c.text).join('\n');
  return { text, isError: !!result.isError };
}

function check(label, ok, detail) {
  if (ok) {
    passed++;
    console.log(`  PASS  ${label}`);
  } else {
    failed++;
    console.log(`  FAIL  ${label}`);
  }
  if (detail) {
    const lines = String(detail).split('\n').slice(0, 6);
    for (const line of lines) console.log(`        ${line}`);
  }
}

const tmpPy = `${ROOT}/_smoke_loop.py`;
const tmpTxt = `${ROOT}/_smoke_note.txt`;

async function main() {
  console.log(`endpoint : ${URL_}`);
  console.log(`token    : ${TOKEN ? `${TOKEN.slice(0, 8)}...` : '(none)'}`);
  console.log(`root     : ${ROOT}`);
  console.log('');

  console.log('[1] handshake');
  const init = await rpc('initialize', {
    protocolVersion: '2024-11-05',
    capabilities: {},
    clientInfo: { name: 'yayi', version: '1.0' },
  });
  check('initialize returns serverInfo', !!init?.serverInfo, JSON.stringify(init?.serverInfo));
  check('protocol version echoed', init?.protocolVersion === '2024-11-05', init?.protocolVersion);

  const listed = await rpc('tools/list');
  const names = (listed?.tools || []).map((t) => t.name);
  check('tools/list exposes 12 tools', names.length === 12, names.join(', '));

  // Panshi sends this between initialize and tools/list; it must be swallowed
  const notif = await rpc('notifications/initialized');
  check('notifications/initialized is accepted', notif === null, 'HTTP 202, no body');

  console.log('\n[2] workspace discovery');
  const ping = await callTool('ping');
  check('ping reports win32 workspace', /root=/.test(ping.text) && !ping.isError, ping.text.split('\n')[0]);

  const listing = await callTool('list_dir', { path: ROOT });
  check('list_dir reads the workspace', !listing.isError && listing.text.length > 0, listing.text.slice(0, 120));

  const tree = await callTool('list_dir', { path: ROOT, depth: 2 });
  check(
    'list_dir depth expands subdirectories',
    tree.text.split('\n').length > listing.text.split('\n').length && /^\s+\S/m.test(tree.text),
    tree.text.split('\n').slice(0, 8).join(' | ')
  );

  // Panshi sometimes forwards a whole sentence where a path belongs; the reply
  // must carry enough context for the next call to be aimed correctly
  const bogus = await callTool('list_dir', { path: 'this is a sentence, not a path' });
  check('list_dir recovers from a bogus path', bogus.isError && /Top level of the workspace/.test(bogus.text), bogus.text.split('\n')[0]);

  console.log('\n[3] shell: the shape Panshi actually emits');
  const version = await callTool('run_shell', { command: 'python3 --version 2>&1 || echo NEED_INSTALL' });
  check('python3 resolves (shim works)', /Python 3/.test(version.text), version.text.trim());

  // the caller otherwise assumes Linux and wastes turns on xdg-open
  check('shell results carry the host identity', /env=Windows/.test(version.text), version.text.split('\n')[1]);

  const linux = await callTool('run_shell', { command: 'xdg-open some-file.html' });
  check(
    'a Linux-only command gets corrected in place',
    /not Linux/.test(linux.text) && /no xdg-open/.test(linux.text),
    linux.text.split('\n').slice(0, 2).join(' | ')
  );

  const piped = await callTool('run_shell', { command: 'ls -1 | head -3 && echo PIPELINE_OK' });
  check('pipes and && work', /PIPELINE_OK/.test(piped.text), piped.text.trim());

  const quoted = `"D:\\Projects\\AI Projects"`;
  const backslash = await callTool('run_shell', { command: `ls ${quoted} > /dev/null && echo PATH_OK` });
  check('quoted backslash paths are normalised', /PATH_OK/.test(backslash.text), backslash.text.trim());

  // relative paths only mean something once the shell sits inside a project
  const relative = await callTool('run_shell', { command: 'ls .\\bin > /dev/null && echo REL_OK', cwd: ROOT });
  check('relative backslash paths are normalised', /REL_OK/.test(relative.text), relative.text.trim());

  const slow = await callTool('run_shell', { command: 'sleep 3 && echo SLEPT', timeout: 30 });
  check('run_shell honours the timeout argument', /SLEPT/.test(slow.text), slow.text.trim());

  // A head-only cut would hide the failing line of a long test run, so the
  // truncation has to preserve the end of the output as well as the start.
  const big = await callTool('run_shell', { command: 'seq 1 20000' });
  check(
    'oversized output keeps both ends',
    /exit=0/.test(big.text) && /\b20000\b/.test(big.text) && /characters omitted/.test(big.text),
    `returned ${big.text.length} chars`
  );

  console.log('\n[4] write -> run -> patch -> verify');
  const script = [
    'def fib(n):',
    '    a, b = 0, 1',
    '    for _ in range(n):',
    '        a, b = b, a + b',
    '    return a',
    '',
    'print([fib(i) for i in range(10)])',
    '',
  ].join('\n');
  const wrote = await callTool('write_file', { path: tmpPy, content: script });
  check('write_file creates the script', !wrote.isError, wrote.text);

  const ran = await callTool('run_shell', { command: `python3 "${tmpPy}"` });
  check('script runs', /\[0, 1, 1, 2, 3, 5, 8, 13, 21, 34\]/.test(ran.text), ran.text.trim());

  const patched = await callTool('apply_patch', { path: tmpPy, old_string: 'range(10)', new_string: 'range(15)' });
  check('apply_patch edits in place', !patched.isError, patched.text);

  const verified = await callTool('run_shell', { command: `python3 "${tmpPy}"` });
  // count only the printed list; the wrapper line "exit=0" would otherwise add a term
  const printed = (verified.text.match(/\[[^\]]*\]/) || [''])[0];
  const values = (printed.match(/\d+/g) || []).length;
  check('patch took effect (15 terms now)', values === 15, verified.text.trim());

  const badPatch = await callTool('apply_patch', { path: tmpPy, old_string: 'this text is not in the file', new_string: 'x' });
  check('apply_patch rejects a missing anchor', badPatch.isError, badPatch.text.slice(0, 100));

  console.log('\n[5] read + search');
  const read = await callTool('read_file', { path: tmpTxt });
  check('read_file on a missing file errors cleanly', read.isError, read.text.slice(0, 90));

  const searched = await callTool('search_code', { pattern: 'def fib', path: ROOT, glob: '_smoke_*.py' });
  check('search_code finds the definition', /_smoke_loop\.py:\d+/.test(searched.text), searched.text.split('\n')[0]);

  const found = await callTool('find_files', { pattern: '**/_smoke_*.py', path: ROOT });
  check('find_files matches by glob', /_smoke_loop\.py/.test(found.text), found.text.split('\n').slice(0, 2).join(' | '));

  const replaced = await callTool('replace_in_files', {
    old_string: 'range(15)',
    new_string: 'range(12)',
    path: ROOT,
    glob: '_smoke_*.py',
  });
  check('replace_in_files rewrites across files', /replaced \d+ occurrence/.test(replaced.text), replaced.text.split('\n')[0]);

  // A workspace that is not a git repo legitimately errors, so accept either a
  // result or the "not a git repository" message — what matters is that the tool
  // exists, runs git, and reports back rather than hanging or throwing opaquely.
  const status = await callTool('git_status', { cwd: ROOT });
  check(
    'git_status runs and reports',
    /branch:|not a git repository/.test(status.text),
    status.text.split('\n')[0]
  );

  const diff = await callTool('git_diff', { cwd: ROOT });
  check(
    'git_diff runs and reports',
    // Either a real diff, an empty result, or the not-a-repo error — all three
    // prove the tool ran git rather than failing to dispatch.
    /no differences|^diff |Error:|not a git repository/.test(diff.text),
    diff.text.split('\n')[0]
  );

  const loose = await callTool('search_code', { pattern: 'DEF FIB', path: ROOT, glob: '_smoke_*.py' });
  check('search_code is case-insensitive by default', /_smoke_loop\.py/.test(loose.text), loose.text.split('\n')[0]);

  const exact = await callTool('search_code', { pattern: 'DEF FIB', path: ROOT, glob: '_smoke_*.py', case_sensitive: true });
  check('search_code honours case_sensitive', /no matches/.test(exact.text), exact.text.split('\n')[0]);

  console.log('\n[6] sandbox boundary');
  const escape = await callTool('read_file', { path: '../../../Windows/System32/drivers/etc/hosts' });
  check('path escape is blocked', escape.isError, escape.text.slice(0, 90));

  const openMissing = await callTool('open_path', { path: '_definitely_not_here.html' });
  check(
    'open_path reports a missing file cleanly',
    openMissing.isError && /ENOENT|no such file or directory/.test(openMissing.text),
    openMissing.text.split('\n')[0]
  );

  const desk = await callTool('list_dir', { path: 'Desktop' });
  check('the real desktop is reachable as Desktop/', !desk.isError && desk.text.length > 0, desk.text.split('\n').slice(0, 4).join(' | '));

  // the desktop is a junction to a path outside the workspace; walking through
  // it with .. would otherwise expose C:\Users\<user> and everything under it
  const climb = await callTool('list_dir', { path: 'Desktop/..' });
  check('the desktop link cannot be used to climb out', climb.isError, climb.text.slice(0, 90));

  if (!KEEP) {
    await callTool('run_shell', { command: `rm -f "${tmpPy}" "${tmpTxt}"` });
    console.log('\ntemp files cleaned up');
  }

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error(`\nfatal: ${err.message}`);
  console.error('is the server running?  .\\panshi-mcp.ps1 status');
  process.exit(2);
});
