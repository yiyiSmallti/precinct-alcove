// @precinct/alcove-bridge — runs inside the dsh process.
//
// dsh's SDK server has no approval or question channel (Alcove validation
// report, section 4). This plugin is the terminal answerer for both and asks
// the Alcove client over the host-callback connection (Charter protocols.md,
// "宿主回连约定"): ALCOVE_BRIDGE_HOST names the endpoint (a named pipe on
// Windows, a Unix socket elsewhere), ALCOVE_BRIDGE_HOST_TOKEN the one-time
// token for this launch. With neither set the plugin registers nothing and
// every request falls through to dsh's fail-closed default.
//
// Wire (JSON-RPC 2.0, one frame per line), protocol "alcove-bridge/1":
//   bridge → host  host.hello { protocol, token, pid }
//   bridge → host  approval.ask { requestId, sessionId, toolName, callId?, reason, displayReason? }
//                    displayReason is dsh localized text: a string or { en, zh, … }
//                    → { outcome: "allowed-once" | "rejected" | "cancelled" }
//   bridge → host  questions.ask { requestId, sessionId, callId?, questions[] }
//                    → { answers: [{ id, selected[], custom? }] }
//   bridge → host  request.cancel { requestId }   (notification: dsh withdrew the request)
import net from 'node:net'
import { randomUUID } from 'node:crypto'

export const name = 'alcove-bridge'
export const PROTOCOL = 'alcove-bridge/1'

class Peer {
  constructor (socket) {
    this.socket = socket
    this.nextId = 0
    this.pending = new Map()
    this.closed = false
    let buf = ''
    socket.setEncoding('utf8')
    socket.on('data', (chunk) => {
      buf += chunk
      let i
      while ((i = buf.indexOf('\n')) >= 0) {
        const line = buf.slice(0, i)
        buf = buf.slice(i + 1)
        let m
        try { m = JSON.parse(line) } catch { continue }
        if (m.id !== undefined && m.method === undefined) {
          const p = this.pending.get(m.id)
          if (!p) continue
          this.pending.delete(m.id)
          if (m.error) p.reject(Object.assign(new Error(m.error.message), { code: m.error.code }))
          else p.resolve(m.result)
        } else if (m.id !== undefined) {
          // The bridge serves no host requests in protocol 1.
          this.send({ jsonrpc: '2.0', id: m.id, error: { code: -32021, message: 'unsupported' } })
        }
      }
    })
    const fail = () => {
      this.closed = true
      for (const p of this.pending.values()) p.reject(new Error('alcove bridge disconnected'))
      this.pending.clear()
    }
    socket.on('close', fail)
    socket.on('error', fail)
  }

  send (frame) {
    if (!this.closed) this.socket.write(JSON.stringify(frame) + '\n')
  }

  request (method, params) {
    if (this.closed) return Promise.reject(new Error('alcove bridge disconnected'))
    const id = ++this.nextId
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject })
      this.send({ jsonrpc: '2.0', id, method, params })
    })
  }

  notify (method, params) {
    this.send({ jsonrpc: '2.0', method, params })
  }
}

// stdout is reserved for the SDK protocol; diagnostics go to stderr.
const warn = (msg) => process.stderr.write(`alcove-bridge: ${msg}\n`)

function connect (endpoint, token) {
  return new Promise((resolve) => {
    const socket = net.connect(endpoint)
    socket.once('error', (err) => {
      warn(`cannot reach ${endpoint}: ${err.message}`)
      resolve(undefined)
    })
    socket.once('connect', async () => {
      const peer = new Peer(socket)
      try {
        await peer.request('host.hello', { protocol: PROTOCOL, token, pid: process.pid })
        resolve(peer)
      } catch (err) {
        warn(`handshake refused: ${err.message}`)
        socket.destroy()
        resolve(undefined)
      }
    })
  })
}

// Ask the host and map an abort of dsh's signal to request.cancel.
async function ask (peerP, method, params, signal) {
  const peer = await peerP
  if (!peer || peer.closed) return undefined
  // Withdrawn while we waited for the connection: an abort event that already
  // fired is not replayed, so never send it.
  if (signal?.aborted) return undefined
  const requestId = randomUUID()
  const onAbort = () => peer.notify('request.cancel', { requestId })
  signal?.addEventListener('abort', onAbort, { once: true })
  try {
    return await peer.request(method, { requestId, ...params })
  } finally {
    signal?.removeEventListener('abort', onAbort)
  }
}

const OUTCOMES = new Set(['allowed-once', 'rejected', 'cancelled'])

export function apply (ctx) {
  const endpoint = process.env.ALCOVE_BRIDGE_HOST
  const token = process.env.ALCOVE_BRIDGE_HOST_TOKEN
  if (!endpoint || !token) return
  // Connect once per process; the token is single-use, so drop it from the
  // environment that tools and child processes inherit.
  delete process.env.ALCOVE_BRIDGE_HOST_TOKEN
  const peerP = connect(endpoint, token)

  ctx.on('approval/request', async (request, next) => {
    let res
    try {
      res = await ask(peerP, 'approval.ask', {
        sessionId: request.agent?.session?.id,
        toolName: request.toolName,
        callId: request.callId,
        reason: request.reason,
        displayReason: request.displayReason
      }, request.signal)
    } catch (err) {
      warn(`approval.ask failed: ${err.message}`)
      return 'unavailable'
    }
    if (res === undefined) return next()
    return OUTCOMES.has(res?.outcome) ? res.outcome : 'unavailable'
  })

  ctx.on('user-questions/request', async (request, next) => {
    let res
    try {
      res = await ask(peerP, 'questions.ask', {
        sessionId: request.agent?.session?.id,
        callId: request.wait?.callId,
        questions: request.questions
      }, request.signal)
    } catch (err) {
      warn(`questions.ask failed: ${err.message}`)
      return next()
    }
    if (res === undefined) return next()
    return { answers: Array.isArray(res?.answers) ? res.answers : [] }
  })
}
