// Builds the release archives uploaded to GitHub Releases, which the online
// upgrade downloads: one archive per platform plus SHA256SUMS.txt.
//
//   npm run build:web && node scripts/release.mjs [outputDir]
//
// Archive layout (the updater relies on it, see server/updater.go):
//   proxy-manager_v<version>_<os>_<arch>/
//     proxy-manager[.exe]  3proxy-install.sh  web/  start.sh | start.cmd
//     .env.example  LICENSE  README.md
//
// The archives are written here rather than with tar/zip so file modes are
// right on every build host (Windows has no executable bit) and the output is
// reproducible: entries are sorted and stamped with the commit time.
import { spawnSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { crc32, deflateRawSync, gzipSync } from 'node:zlib'

const repo = fileURLToPath(new URL('..', import.meta.url))
const output = resolve(process.argv[2] || join(repo, 'dist', 'release'))
const { version } = JSON.parse(readFileSync(join(repo, 'package.json'), 'utf8'))
const targets = [
  ['linux', 'amd64'],
  ['linux', 'arm64'],
  ['windows', 'amd64'],
  ['darwin', 'amd64'],
  ['darwin', 'arm64'],
]

const startSh = `#!/bin/sh
# Starts Proxy Manager with the bundled web console and node installer.
# All settings come from environment variables (see .env.example / README).
DIR=$(cd "$(dirname "$0")" && pwd)
cd "$DIR" || exit 1
: "\${STATIC_DIR:=$DIR/web}"
: "\${INSTALL_SCRIPT_PATH:=$DIR/3proxy-install.sh}"
export STATIC_DIR INSTALL_SCRIPT_PATH
exec "$DIR/proxy-manager" "$@"
`

const startCmd = [
  '@echo off',
  'setlocal',
  'cd /d "%~dp0"',
  'if not defined STATIC_DIR set "STATIC_DIR=%~dp0web"',
  'if not defined INSTALL_SCRIPT_PATH set "INSTALL_SCRIPT_PATH=%~dp03proxy-install.sh"',
  '"%~dp0proxy-manager.exe" %*',
  '',
].join('\r\n')

function git(...args) {
  const result = spawnSync('git', ['-C', repo, ...args], { maxBuffer: 64 << 20 })
  if (result.status !== 0) throw new Error(`git ${args.join(' ')} failed: ${result.stderr}`)
  return result.stdout
}

function goBuild(goos, goarch, destination) {
  const result = spawnSync('go', ['-C', join(repo, 'server'), 'build', '-trimpath', '-ldflags=-s -w', '-o', destination, '.'], {
    stdio: 'inherit',
    windowsHide: true,
    env: { ...process.env, GOOS: goos, GOARCH: goarch, CGO_ENABLED: '0' },
  })
  if (result.error) throw result.error
  if (result.status !== 0) throw new Error(`go build for ${goos}/${goarch} failed`)
  return readFileSync(destination)
}

/** Files of a directory as [relativePath, content], sorted, with / separators. */
function walk(directory, prefix = '') {
  return readdirSync(directory, { withFileTypes: true })
    .sort((left, right) => left.name.localeCompare(right.name))
    .flatMap((entry) => entry.isDirectory()
      ? walk(join(directory, entry.name), `${prefix}${entry.name}/`)
      : [[`${prefix}${entry.name}`, readFileSync(join(directory, entry.name))]])
}

/** Adds the parent directories of every file, so extractors create them with mode 0755. */
function withDirectories(root, files) {
  const entries = new Map([[`${root}/`, { directory: true }]])
  for (const file of files) {
    const parts = file.path.split('/')
    for (let index = 1; index < parts.length; index += 1) {
      entries.set(`${root}/${parts.slice(0, index).join('/')}/`, { directory: true })
    }
    entries.set(`${root}/${file.path}`, file)
  }
  return [...entries].sort(([left], [right]) => (left < right ? -1 : 1)).map(([path, entry]) => ({ ...entry, path }))
}

function tarHeader(path, mode, size, mtime, directory) {
  if (Buffer.byteLength(path) > 100) throw new Error(`archive path too long for ustar: ${path}`)
  const header = Buffer.alloc(512)
  const field = (offset, length, value) => header.write(value, offset, length, 'utf8')
  const octal = (offset, length, value) => field(offset, length, `${value.toString(8).padStart(length - 1, '0')}\0`)
  field(0, 100, path)
  octal(100, 8, mode)
  octal(108, 8, 0)
  octal(116, 8, 0)
  octal(124, 12, size)
  octal(136, 12, mtime)
  field(148, 8, '        ')
  field(156, 1, directory ? '5' : '0')
  field(257, 6, 'ustar\0')
  field(263, 2, '00')
  let checksum = 0
  for (const byte of header) checksum += byte
  field(148, 8, `${checksum.toString(8).padStart(6, '0')}\0 `)
  return header
}

function tarGz(entries, mtime) {
  const blocks = []
  for (const entry of entries) {
    const content = entry.directory ? Buffer.alloc(0) : entry.content
    blocks.push(tarHeader(entry.path, entry.directory ? 0o755 : entry.mode, content.length, mtime, entry.directory))
    blocks.push(content, Buffer.alloc((512 - (content.length % 512)) % 512))
  }
  blocks.push(Buffer.alloc(1024))
  return gzipSync(Buffer.concat(blocks), { level: 9 })
}

function zip(entries, mtime) {
  const date = new Date(mtime * 1000)
  const dosTime = (date.getUTCHours() << 11) | (date.getUTCMinutes() << 5) | Math.floor(date.getUTCSeconds() / 2)
  const dosDate = ((date.getUTCFullYear() - 1980) << 9) | ((date.getUTCMonth() + 1) << 5) | date.getUTCDate()
  const locals = []
  const centrals = []
  let offset = 0
  for (const entry of entries) {
    const name = Buffer.from(entry.path, 'utf8')
    const content = entry.directory ? Buffer.alloc(0) : entry.content
    const compressed = entry.directory ? content : deflateRawSync(content, { level: 9 })
    const method = entry.directory ? 0 : 8
    const checksum = crc32(content)
    const local = Buffer.alloc(30)
    local.writeUInt32LE(0x04034b50, 0)
    local.writeUInt16LE(20, 4)
    local.writeUInt16LE(0x0800, 6)
    local.writeUInt16LE(method, 8)
    local.writeUInt16LE(dosTime, 10)
    local.writeUInt16LE(dosDate, 12)
    local.writeUInt32LE(checksum, 14)
    local.writeUInt32LE(compressed.length, 18)
    local.writeUInt32LE(content.length, 22)
    local.writeUInt16LE(name.length, 26)
    locals.push(local, name, compressed)

    const central = Buffer.alloc(46)
    central.writeUInt32LE(0x02014b50, 0)
    central.writeUInt16LE((3 << 8) | 20, 4) // made by Unix, so the modes below apply
    central.writeUInt16LE(20, 6)
    central.writeUInt16LE(0x0800, 8)
    central.writeUInt16LE(method, 10)
    central.writeUInt16LE(dosTime, 12)
    central.writeUInt16LE(dosDate, 14)
    central.writeUInt32LE(checksum, 16)
    central.writeUInt32LE(compressed.length, 20)
    central.writeUInt32LE(content.length, 24)
    central.writeUInt16LE(name.length, 28)
    const unixMode = entry.directory ? 0o40755 : 0o100000 | entry.mode
    central.writeUInt32LE(((unixMode << 16) | (entry.directory ? 0x10 : 0)) >>> 0, 38)
    central.writeUInt32LE(offset, 42)
    centrals.push(central, name)
    offset += local.length + name.length + compressed.length
  }
  const directory = Buffer.concat(centrals)
  const end = Buffer.alloc(22)
  end.writeUInt32LE(0x06054b50, 0)
  end.writeUInt16LE(entries.length, 8)
  end.writeUInt16LE(entries.length, 10)
  end.writeUInt32LE(directory.length, 12)
  end.writeUInt32LE(offset, 16)
  return Buffer.concat([...locals, directory, end])
}

const webDist = join(repo, 'web', 'dist')
const webFiles = walk(webDist).map(([path, content]) => ({ path: `web/${path}`, content, mode: 0o644 }))
if (!webFiles.some((file) => file.path === 'web/index.html')) {
  throw new Error('web/dist is missing; run npm run build:web first')
}
// Committed content (LF line endings), independent of core.autocrlf.
const documents = ['.env.example', '3proxy-install.sh', 'LICENSE', 'README.md']
  .map((path) => ({ path, content: git('show', `HEAD:${path}`), mode: path.endsWith('.sh') ? 0o755 : 0o644 }))
const mtime = Number(git('log', '-1', '--format=%ct').toString().trim())

rmSync(output, { recursive: true, force: true })
mkdirSync(join(output, 'bin'), { recursive: true })
const sums = []
for (const [goos, goarch] of targets) {
  const root = `proxy-manager_v${version}_${goos}_${goarch}`
  const windows = goos === 'windows'
  const binary = windows ? 'proxy-manager.exe' : 'proxy-manager'
  const files = [
    ...documents,
    ...webFiles,
    { path: binary, content: goBuild(goos, goarch, join(output, 'bin', `${root}-${binary}`)), mode: 0o755 },
    windows
      ? { path: 'start.cmd', content: Buffer.from(startCmd), mode: 0o644 }
      : { path: 'start.sh', content: Buffer.from(startSh), mode: 0o755 },
  ]
  const entries = withDirectories(root, files)
  const archive = `${root}${windows ? '.zip' : '.tar.gz'}`
  const content = windows ? zip(entries, mtime) : tarGz(entries, mtime)
  writeFileSync(join(output, archive), content)
  sums.push(`${createHash('sha256').update(content).digest('hex')}  ${archive}`)
  console.log(`built ${archive} (${content.length} bytes)`)
}
writeFileSync(join(output, 'SHA256SUMS.txt'), `${sums.join('\n')}\n`)
rmSync(join(output, 'bin'), { recursive: true, force: true })
console.log(`release v${version} written to ${output}`)
