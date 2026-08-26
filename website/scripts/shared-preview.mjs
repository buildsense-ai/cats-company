import { execFileSync, spawn } from 'node:child_process'
import { createRequire } from 'node:module'
import { dirname, join } from 'node:path'

const worktreeOutput = execFileSync('git', ['worktree', 'list', '--porcelain'], {
  cwd: process.cwd(),
  encoding: 'utf8',
})

const primaryRoot = worktreeOutput
  .split(/\r?\n/)
  .find((line) => line.startsWith('worktree '))
  ?.slice('worktree '.length)

if (!primaryRoot) {
  throw new Error('Unable to locate the primary CatsCo worktree.')
}

const port = process.env.CATSCO_SHARED_PREVIEW_PORT || '5175'
const requireFromRoot = createRequire(join(primaryRoot, 'package.json'))
const vitePackage = requireFromRoot.resolve('vite/package.json')
const viteBin = join(dirname(vitePackage), 'bin', 'vite.js')
const branch = execFileSync('git', ['-C', primaryRoot, 'branch', '--show-current'], { encoding: 'utf8' }).trim()

console.log(`[CatsCo shared preview] root: ${primaryRoot}`)
console.log(`[CatsCo shared preview] branch: ${branch || '(detached)'}`)
console.log(`[CatsCo shared preview] url: http://127.0.0.1:${port}`)

const child = spawn(
  process.execPath,
  [viteBin, primaryRoot, '--host', '127.0.0.1', '--port', port, '--strictPort'],
  { cwd: primaryRoot, stdio: 'inherit' },
)

for (const signal of ['SIGINT', 'SIGTERM']) {
  process.on(signal, () => child.kill(signal))
}

child.on('exit', (code, signal) => {
  if (signal) process.kill(process.pid, signal)
  else process.exit(code ?? 1)
})
