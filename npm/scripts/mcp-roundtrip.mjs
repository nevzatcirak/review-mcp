#!/usr/bin/env node
// Smoke check: drives an installed review-mcp launcher over stdio with an
// MCP initialize -> tools/list round trip and compares the tool names.
//
// Usage: node npm/scripts/mcp-roundtrip.mjs <install-dir> <manifest.json>
//
// The launcher is node_modules/<main package>/bin/review-mcp.js inside
// <install-dir>; the main package name comes from the manifest, so the scope
// is not repeated here. The server is started with placeholder configuration
// only: tools/list touches no network.

import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { createInterface } from 'node:readline';

// The ten tools the stdio server registers (P6 spec §3.3, acceptance A4;
// X-14 added pr_comment_create, X-16 added job_result, which serve mode does
// not register, X-23 added pr_info, X-26 added pr_describe, X-27 added
// pr_improve).
const EXPECTED_TOOLS = ['job_result', 'pr_ask', 'pr_comment_create', 'pr_comment_reply', 'pr_comments', 'pr_describe', 'pr_improve', 'pr_info', 'pr_review', 'server_info'];

const TIMEOUT_MS = 30000;

function fail(message) {
  process.stderr.write(`mcp-roundtrip: ${message}\n`);
  process.exit(1);
}

const [installDir, manifestFile] = process.argv.slice(2);
if (!installDir || !manifestFile) fail('usage: mcp-roundtrip.mjs <install-dir> <manifest.json>');

const manifest = JSON.parse(fs.readFileSync(manifestFile, 'utf8'));
const mainName = manifest.packages[manifest.packages.length - 1].name;
const launcher = path.join(installDir, 'node_modules', ...mainName.split('/'), 'bin', 'review-mcp.js');
if (!fs.existsSync(launcher)) fail(`launcher not found: ${launcher}`);

// Placeholder configuration: valid enough to start, never contacted.
const env = {
  ...process.env,
  REVIEW_MCP_LLM_BASE_URL: 'https://llm.example.com/v1',
  REVIEW_MCP_LLM_MODEL: 'example-model',
  REVIEW_MCP_LLM_CONTEXT_WINDOW: '32000',
  REVIEW_MCP_LLM_API_KEY: 'placeholder-not-a-secret',
  REVIEW_MCP_GITEA_BASE_URL: 'https://your-gitea.example',
  REVIEW_MCP_GITEA_TOKEN: 'placeholder-not-a-secret',
};

const child = spawn(process.execPath, [launcher], { env, stdio: ['pipe', 'pipe', 'inherit'] });
const timer = setTimeout(() => {
  child.kill();
  fail(`timed out after ${TIMEOUT_MS} ms`);
}, TIMEOUT_MS);

const pending = new Map();
const lines = createInterface({ input: child.stdout });
lines.on('line', (line) => {
  let message;
  try {
    message = JSON.parse(line);
  } catch {
    child.kill();
    fail('stdout carried a line that is not JSON (stdout must hold only MCP messages)');
    return;
  }
  const waiter = pending.get(message.id);
  if (waiter) {
    pending.delete(message.id);
    waiter(message);
  }
});

function send(message) {
  child.stdin.write(`${JSON.stringify(message)}\n`);
}

function request(id, method, params) {
  return new Promise((resolve) => {
    pending.set(id, resolve);
    send({ jsonrpc: '2.0', id, method, params });
  });
}

const exited = new Promise((resolve) => child.on('exit', (code, signal) => resolve({ code, signal })));

const init = await request(1, 'initialize', {
  protocolVersion: '2025-03-26',
  capabilities: {},
  clientInfo: { name: 'npm-smoke', version: '0.0.0' },
});
if (!init.result || !init.result.serverInfo) fail(`unexpected initialize reply: ${JSON.stringify(init)}`);
send({ jsonrpc: '2.0', method: 'notifications/initialized' });

const list = await request(2, 'tools/list', {});
if (!list.result || !Array.isArray(list.result.tools)) fail(`unexpected tools/list reply: ${JSON.stringify(list)}`);
const names = list.result.tools.map((t) => t.name).sort();

child.stdin.end();
const { code, signal } = await exited;
clearTimeout(timer);

if (JSON.stringify(names) !== JSON.stringify(EXPECTED_TOOLS)) {
  fail(`tools mismatch: got [${names.join(', ')}], want [${EXPECTED_TOOLS.join(', ')}]`);
}
if (code !== 0) fail(`launcher exited with code ${code} signal ${signal} after stdin closed`);
process.stderr.write(`mcp-roundtrip: ok, server ${init.result.serverInfo.name} lists ${names.length} tools: ${names.join(', ')}\n`);
