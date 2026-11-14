// Says who looks up or connects to anything but this machine: loaded into
// every Node process of a test run (NODE_OPTIONS=--require, see personal.sh
// PERSONAL_NETTRACE=1), it appends the host, the process and the call stack
// to NETTRACE_LOG. A packet capture shows that something left; this shows
// which code sent it. Diagnostic only: it is not part of what is verified.
const fs = require('fs'), dns = require('dns'), net = require('net');
const log = process.env.NETTRACE_LOG;
const here = (h) => !h || h === 'localhost' || /^127\.|^::1$|^\[::1\]$/.test(String(h));
const note = (kind, host) => {
  if (!log || here(host)) return;
  const stack = new Error().stack.split('\n').slice(3, 15).map((s) => s.trim()).join('\n    ');
  try {
    fs.appendFileSync(log, `\n## ${kind} ${host}\n  pid ${process.pid} ppid ${process.ppid} cwd ${process.cwd()}\n  argv ${process.argv.slice(1, 8).join(' ')}\n    ${stack}\n`);
  } catch {}
};
const wrap = (obj, name, kind) => {
  const orig = obj[name];
  obj[name] = function (host, ...rest) { note(kind, host); return orig.call(this, host, ...rest); };
};
for (const m of ['lookup', 'resolve', 'resolve4', 'resolve6']) { wrap(dns, m, 'dns.' + m); wrap(dns.promises, m, 'dns.promises.' + m); }
const connect = net.Socket.prototype.connect;
net.Socket.prototype.connect = function (...a) {
  const o = Array.isArray(a[0]) ? a[0][0] : a[0];
  if (o && typeof o === 'object' && o.host) note('connect', `${o.host}:${o.port}`);
  return connect.apply(this, a);
};
