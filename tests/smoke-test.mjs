#!/usr/bin/env node
/**
 * End-to-end smoke test for the ScienceOne desktop bridge.
 *
 * Speaks the same JSON-RPC the platform sends (protocol 2024-11-05) and
 * exercises every tool against a real workspace, so a green run proves the whole
 * path works: the platform -> public tunnel -> this machine.
 *
 * Usage:
 *   node tests/smoke-test.mjs
 *   node tests/smoke-test.mjs --url http://127.0.0.1:8802/mcp --token <token>
 *   node tests/smoke-test.mjs --url https://xxx.trycloudflare.com/mcp --token <token>
 *
 * Flags:
 *   --url     MCP endpoint
 *   --token   X-MCP-Token value
 *   --root    workspace-relative directory used for temp files
 *   --keep    leave the temp files behind for inspection
 */

import process from 'node:process';

const args = process.argv.slice(2);
function argOf(flag, fallback) {
  const i = args.indexOf(flag);
  return i >= 0 && args[i + 1] ? args[i + 1] : fallback;
}
const URL_ = argOf('--url', 'http://127.0.0.1:8802/mcp');
const TOKEN = argOf('--token', process.env.MCP_TOKEN || '');
const ROOT = argOf('--root', 'AI Projects/DSH/new-api/tools/panshi-mcp');
const KEEP = args.includes('--keep');

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
    const lines = String(detail).split('\n').slice(0, 5);
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
  check(
    'tool names follow the mainstream convention',
    ['read', 'readLines', 'write', 'edit', 'glob', 'grep', 'todoWrite', 'bash', 'pwsh'].every((n) =>
      names.includes(n)
    ),
    names.join(', ')
  );

  const notif = await rpc('notifications/initialized');
  check('notifications/initialized is accepted', notif === null, 'HTTP 202, no body');

  console.log('\n[2] workspace discovery');
  const ping = await callTool('ping');
  check('ping reports win32 workspace', /root=/.test(ping.text) && !ping.isError, ping.text.split('\n')[0]);

  const listing = await callTool('ls', { path: ROOT });
  check('ls reads the workspace', !listing.isError && listing.text.length > 0, listing.text.slice(0, 100));

  const tree = await callTool('ls', { path: ROOT, depth: 2 });
  check(
    'ls depth expands subdirectories',
    tree.text.split('\n').length > listing.text.split('\n').length && /^\s+\S/m.test(tree.text),
    tree.text.split('\n').slice(0, 6).join(' | ')
  );

  const bogus = await callTool('ls', { path: 'this is a sentence, not a path' });
  check(
    'ls recovers from a bogus path',
    bogus.isError && /Top level of the workspace/.test(bogus.text),
    bogus.text.split('\n')[0]
  );

  console.log('\n[3] bash: the shape the platform actually emits');
  const version = await callTool('bash', { command: 'python3 --version 2>&1 || echo NEED_INSTALL' });
  check('python3 resolves (shim works)', /Python 3/.test(version.text), version.text.trim());

  check('shell results carry the host identity', /env=Windows/.test(version.text), version.text.split('\n')[1]);

  const linux = await callTool('bash', { command: 'xdg-open some-file.html' });
  check(
    'a Linux-only command gets corrected in place',
    /not Linux/.test(linux.text) && /no xdg-open/.test(linux.text),
    linux.text.split('\n').slice(0, 2).join(' | ')
  );

  const piped = await callTool('bash', { command: 'ls -1 | head -3 && echo PIPELINE_OK' });
  check('pipes and && work', /PIPELINE_OK/.test(piped.text), piped.text.split('\n').slice(0, 3).join(' | '));

  const quoted = await callTool('bash', { command: `ls "D:\\Projects\\AI Projects" > /dev/null && echo PATH_OK` });
  check('quoted backslash paths are normalised', /PATH_OK/.test(quoted.text), quoted.text.trim());

  const relative = await callTool('bash', { command: 'ls .\\bin > /dev/null && echo REL_OK', cwd: ROOT });
  check('relative backslash paths are normalised', /REL_OK/.test(relative.text), relative.text.trim());

  const nonzero = await callTool('bash', { command: 'exit 3' });
  check('a non-zero exit is reported, not treated as failure', /\[exit code: 3\]/.test(nonzero.text), nonzero.text.trim());

  const slow = await callTool('bash', { command: 'sleep 3 && echo SLEPT', timeout: 30 });
  check('bash honours the timeout argument', /SLEPT/.test(slow.text), slow.text.trim());

  const big = await callTool('bash', { command: 'seq 1 20000' });
  check(
    'oversized output keeps both ends',
    /\b20000\b/.test(big.text) && /characters omitted/.test(big.text),
    `returned ${big.text.length} chars`
  );

  const ps = await callTool('pwsh', { command: '$PSVersionTable.PSVersion.Major' });
  check('pwsh runs PowerShell', /\d/.test(ps.text) && !ps.isError, ps.text.split('\n').slice(0, 2).join(' | '));

  console.log('\n[4] write -> read -> edit -> verify');
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
  const wrote = await callTool('write', { path: tmpPy, content: script });
  check('write creates the script', !wrote.isError, wrote.text);

  const ran = await callTool('bash', { command: `python3 "${tmpPy}"` });
  check('script runs', /\[0, 1, 1, 2, 3, 5, 8, 13, 21, 34\]/.test(ran.text), ran.text.trim());

  const readBack = await callTool('read', { path: tmpPy, offset: 1, limit: 2 });
  check('read honours offset/limit', /lines 1-2 of 8/.test(readBack.text), readBack.text.split('\n')[0]);

  const structured = await callTool('readLines', { path: tmpPy, offset: 1, limit: 2 });
  check(
    'readLines returns a structured array',
    /"lines"/.test(structured.text) && /"totalLines": 8/.test(structured.text),
    structured.text.split('\n').slice(0, 3).join(' | ')
  );

  const patched = await callTool('edit', { path: tmpPy, oldString: 'range(10)', newString: 'range(15)' });
  check('edit accepts camelCase parameters', !patched.isError, patched.text);

  const verified = await callTool('bash', { command: `python3 "${tmpPy}"` });
  const printed = (verified.text.match(/\[[^\]]*\]/) || [''])[0];
  check('edit took effect (15 terms now)', (printed.match(/\d+/g) || []).length === 15, verified.text.trim());

  const snake = await callTool('edit', { path: tmpPy, old_string: 'range(15)', new_string: 'range(11)' });
  check('edit also accepts snake_case parameters', !snake.isError, snake.text);

  const badPatch = await callTool('edit', { path: tmpPy, oldString: 'this text is not in the file', newString: 'x' });
  check('edit rejects a missing anchor', badPatch.isError, badPatch.text.slice(0, 90));

  console.log('\n[5] search + task list');
  const searched = await callTool('grep', { pattern: 'def fib', path: ROOT, glob: '_smoke_*.py' });
  check('grep finds the definition', /_smoke_loop\.py:\d+/.test(searched.text), searched.text.split('\n')[0]);

  const found = await callTool('glob', { pattern: '**/_smoke_*.py', path: ROOT });
  check('glob matches by pattern', /_smoke_loop\.py/.test(found.text), found.text.split('\n').slice(0, 2).join(' | '));

  const loose = await callTool('grep', { pattern: 'DEF FIB', path: ROOT, glob: '_smoke_*.py' });
  check('grep is case-insensitive by default', /_smoke_loop\.py/.test(loose.text), loose.text.split('\n')[0]);

  const exact = await callTool('grep', {
    pattern: 'DEF FIB',
    path: ROOT,
    glob: '_smoke_*.py',
    caseSensitive: true,
  });
  check('grep honours caseSensitive', /no matches/.test(exact.text), exact.text.split('\n')[0]);

  const todos = await callTool('todoWrite', {
    todos: [
      { content: 'read the module', status: 'completed' },
      { content: 'apply the fix', status: 'in_progress' },
      { content: 'run the tests', status: 'pending' },
    ],
  });
  check('todoWrite renders the list', /\[x\] read the module/.test(todos.text) && /1\/3 done/.test(todos.text), todos.text.split('\n')[0]);

  const badTodo = await callTool('todoWrite', { todos: [{ content: 'x', status: 'nonsense' }] });
  check('todoWrite rejects a bad status', badTodo.isError, badTodo.text.slice(0, 80));

  console.log('\n[6] sandbox boundary');
  const escape = await callTool('read', { path: '../../../Windows/System32/drivers/etc/hosts' });
  check('path escape is blocked', escape.isError, escape.text.slice(0, 90));

  const openMissing = await callTool('open', { path: '_definitely_not_here.html' });
  check(
    'open reports a missing file cleanly',
    openMissing.isError && /ENOENT|no such file or directory/.test(openMissing.text),
    openMissing.text.split('\n')[0]
  );

  const desk = await callTool('ls', { path: 'Desktop' });
  check('the real desktop is reachable as Desktop/', !desk.isError && desk.text.length > 0, desk.text.split('\n').slice(0, 3).join(' | '));

  const climb = await callTool('ls', { path: 'Desktop/..' });
  check('the desktop link cannot be used to climb out', climb.isError, climb.text.slice(0, 90));

  if (!KEEP) {
    await callTool('bash', { command: `rm -f "${tmpPy}" "${tmpTxt}"` });
    console.log('\ntemp files cleaned up');
  }

  console.log(`\n${passed} passed, ${failed} failed`);
  process.exit(failed === 0 ? 0 : 1);
}

main().catch((err) => {
  console.error(`\nfatal: ${err.message}`);
  process.exit(2);
});
