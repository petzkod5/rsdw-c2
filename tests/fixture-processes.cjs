const {spawn, spawnSync} = require('node:child_process');
const {once} = require('node:events');
const path = require('node:path');

// POSIX groups children with a detached process group; Windows has no process groups, so kill the tree by pid.
const canDetach = process.platform !== 'win32';

// The server binary built by verify.sh; Go adds .exe on Windows.
function serverBinary(root) {
  return path.join(root, '.tmp', process.platform === 'win32' ? 'rsdw-c2.exe' : 'rsdw-c2');
}

function killTree(child, detached, signal = 'SIGTERM') {
  if (process.platform === 'win32') spawnSync('taskkill', ['/pid', String(child.pid), '/T', '/F'], {stdio: 'ignore'});
  else if (detached) process.kill(-child.pid, signal);
  else child.kill(signal);
}

function fixtureProcesses(root) {
  const children = [];

  async function start(command, args, env, pattern, detached = false) {
    const child = spawn(command, args, {cwd:root, env:{...process.env, ...env}, detached: detached && canDetach, stdio:['ignore','pipe','pipe']});
    const exited = once(child, 'exit');
    children.push({child, exited, detached});
    return new Promise((resolve, reject) => {
      let logs = '';
      const timeout = setTimeout(() => reject(new Error(`Fixture did not start: ${logs}`)), 30000);
      child.on('error', (error) => { clearTimeout(timeout); reject(error); });
      child.on('exit', (code) => { clearTimeout(timeout); reject(new Error(`Fixture exited ${code}: ${logs}`)); });
      const read = (chunk) => {
        logs += chunk;
        const match = logs.match(pattern);
        if (match) { clearTimeout(timeout); resolve(match[1]); }
      };
      child.stdout.on('data', read);
      child.stderr.on('data', read);
    });
  }

  async function close() {
    for (const {child, exited, detached} of children.reverse()) {
      if (child.exitCode === null && child.signalCode === null) {
        killTree(child, detached);
      }
      await exited;
    }
  }

  return {start, close};
}

module.exports = {fixtureProcesses, killTree, canDetach, serverBinary};
