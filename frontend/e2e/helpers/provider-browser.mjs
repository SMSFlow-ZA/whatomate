// Test-only loopback origin: keep browser crypto/cookies representative while
// the runner replaces the owned upstream HTTP container between test phases.
import http from 'node:http'
import net from 'node:net'
import { spawn } from 'node:child_process'
import { resolve } from 'node:path'
import process from 'node:process'

if (process.env.INBOX_PROVIDER_ACCEPTANCE !== 'synthetic-owned')
  throw new Error('Explicit owned provider acceptance required')
const upstream = new URL(process.env.INBOX_PROVIDER_UPSTREAM || '')
if (
  upstream.origin !== 'http://whatomate-inbox-fork-browser-app:8080' ||
  upstream.pathname !== '/' ||
  upstream.search ||
  upstream.hash ||
  upstream.username ||
  upstream.password ||
  process.env.BASE_URL !== 'http://127.0.0.1:8080'
)
  throw new Error(
    'Only the named owned upstream and loopback browser origin are permitted',
  )

const server = http.createServer((request, response) => {
  const remote = http.request(
    {
      hostname: upstream.hostname,
      port: upstream.port,
      path: request.url,
      method: request.method,
      headers: request.headers,
    },
    (result) => {
      response.writeHead(result.statusCode || 502, result.headers)
      result.pipe(response)
    },
  )
  remote.on('error', () => {
    if (!response.headersSent)
      response.writeHead(503, { 'Content-Type': 'application/json' })
    response.end('{"message":"Owned test process is restarting"}')
  })
  request.on('aborted', () => remote.destroy())
  response.on('close', () => remote.destroy())
  request.pipe(remote)
})
server.on('upgrade', (request, socket, head) => {
  if (request.url !== '/ws') {
    socket.destroy()
    return
  }
  const remote = net.connect(
    { host: upstream.hostname, port: Number(upstream.port) },
    () => {
      remote.write(
        `${request.method} ${request.url} HTTP/${request.httpVersion}\r\n` +
          request.rawHeaders.reduce(
            (text, value, index, values) =>
              index % 2 === 0
                ? text + value + ': ' + values[index + 1] + '\r\n'
                : text,
            '',
          ) +
          '\r\n',
      )
      if (head.length) remote.write(head)
      remote.pipe(socket)
      socket.pipe(remote)
    },
  )
  remote.on('error', () => socket.destroy())
  socket.on('error', () => remote.destroy())
  socket.on('close', () => remote.destroy())
})
await new Promise((done, failed) => {
  server.once('error', failed)
  server.listen(8080, '127.0.0.1', done)
})
const child = spawn(
  process.execPath,
  [resolve('node_modules/@playwright/test/cli.js'), ...process.argv.slice(2)],
  { stdio: 'inherit' },
)
for (const signal of ['SIGINT', 'SIGTERM'])
  process.on(signal, () => child.kill(signal))
child.on('error', () => {
  server.close()
  process.exitCode = 1
})
child.on('exit', (code) => {
  server.close()
  process.exitCode = code ?? 1
})
