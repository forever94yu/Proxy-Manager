import { mkdirSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const outputDir = fileURLToPath(new URL('../dist/', import.meta.url))
mkdirSync(outputDir, { recursive: true })

const binaryName = process.platform === 'win32' ? 'proxy-manager.exe' : 'proxy-manager'
const result = spawnSync('go', ['-C', 'server', 'build', '-trimpath', '-o', `../dist/${binaryName}`, '.'], {
  stdio: 'inherit',
  windowsHide: true,
})

if (result.error) throw result.error
process.exitCode = result.status ?? 1
