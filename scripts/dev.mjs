import { spawn } from 'node:child_process'
import { existsSync, readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

if (existsSync('.env')) {
  for (const sourceLine of readFileSync('.env', 'utf8').split(/\r?\n/)) {
    const line = sourceLine.trim()
    if (!line || line.startsWith('#')) continue
    const separator = line.indexOf('=')
    if (separator <= 0) continue
    const key = line.slice(0, separator).trim()
    let value = line.slice(separator + 1).trim()
    if (!/^[A-Z_][A-Z0-9_]*$/.test(key)) continue
    if ((value.startsWith('"') && value.endsWith('"')) || (value.startsWith("'") && value.endsWith("'"))) {
      value = value.slice(1, -1)
    }
    if (process.env[key] === undefined) process.env[key] = value
  }
}

const viteEntrypoint = fileURLToPath(new URL('../web/node_modules/vite/bin/vite.js', import.meta.url))
const commands = [
  { name: 'api', command: 'go', args: ['-C', 'server', 'run', '.'] },
  { name: 'web', command: process.execPath, args: [viteEntrypoint], cwd: 'web' },
]

const children = commands.map(({ name, command, args, cwd }) => {
  const child = spawn(command, args, {
    cwd,
    env: process.env,
    stdio: 'inherit',
    windowsHide: true,
  })
  child.on('error', (error) => {
    console.error(`[${name}] ${error.message}`)
  })
  return child
})

let stopping = false
function stop(exitCode = 0) {
  if (stopping) return
  stopping = true
  for (const child of children) {
    if (!child.killed) child.kill('SIGTERM')
  }
  setTimeout(() => process.exit(exitCode), 1000).unref()
}

for (const child of children) {
  child.on('exit', (code, signal) => {
    if (stopping) return
    if (signal || code !== 0) stop(code || 1)
  })
}

process.on('SIGINT', () => stop(0))
process.on('SIGTERM', () => stop(0))
