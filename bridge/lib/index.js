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
//   bridge → host  session.live { sessionId, kind, … }   (notification)
//                    kind "chunk" { turn, step, attempt, chunk }: one chunk of the model's reply
//                    as it streams (text-delta, reasoning-delta, tool-call-delta);
//                    kind "abandon" { turn, step, attempt }: that reply attempt was dropped;
//                    kind "tool-output" { callId, text, lossy? }: text a call's running command
//                    printed since the last one (lossy: bytes before it were dropped);
//                    kind "compaction" { compactionId, call, text }: text of the summary a
//                    compaction is writing (call: the summary call, which a retry starts over)
//   host → bridge  session.cancel { sessionId } → { cancelled }   cancels the session's running turn and
//                    drops its queued messages (plugin sessions, Alcove session.cancel)
import net from 'node:net'
import { randomUUID } from 'node:crypto'
import { readFileSync, statSync } from 'node:fs'
import { extname, join } from 'node:path'

export const name = 'alcove-bridge'
export const PROTOCOL = 'alcove-bridge/1'

class Peer {
  constructor (socket, serve) {
    this.socket = socket
    this.serve = serve
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
          const id = m.id
          Promise.resolve()
            .then(() => this.serve(m.method, m.params ?? {}))
            .then(
              (result) => this.send({ jsonrpc: '2.0', id, result }),
              (err) => this.send({ jsonrpc: '2.0', id, error: { code: err?.code ?? -32603, message: String(err?.message ?? err) } })
            )
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

function connect (endpoint, token, serve) {
  return new Promise((resolve) => {
    const socket = net.connect(endpoint)
    socket.once('error', (err) => {
      warn(`cannot reach ${endpoint}: ${err.message}`)
      resolve(undefined)
    })
    socket.once('connect', async () => {
      const peer = new Peer(socket, serve)
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

// The personal context's profile (Alcove SPEC 3.1 个人上下文): in a personal
// workspace's runtime, ALCOVE_PERSONAL_HOME names the folder, and its
// AGENTS.md goes to the model as runtime context, read again at each step
// (cached by size and mtime), so an edit reaches the next step of a running
// session. Runtime context is a message after the cached history, not the
// system prompt, so a change costs no prompt cache.
const PROFILE_MAX = 16 << 10
const PROFILE_INTRO = 'About the user (their personal context in Alcove, learned from their work and editable by them). Treat it as their standing preferences; what they ask in this conversation comes first.'

function personalProfile (ctx) {
  const home = process.env.ALCOVE_PERSONAL_HOME
  if (!home) return
  const file = join(home, 'AGENTS.md')
  let seen = { mtimeMs: -1, size: -1, text: '' }
  const text = () => {
    let st
    try { st = statSync(file) } catch { return '' }
    if (st.mtimeMs !== seen.mtimeMs || st.size !== seen.size) {
      let body = ''
      try { body = readFileSync(file, 'utf8').slice(0, PROFILE_MAX).trim() } catch {}
      seen = { mtimeMs: st.mtimeMs, size: st.size, text: body }
    }
    return seen.text ? `${PROFILE_INTRO}\n\n${seen.text}` : ''
  }
  ctx.inject(['systemPrompt'], (ctx) => {
    ctx.systemPrompt.context({ name: 'alcove:personal-profile', order: 130, interpolate: false, text })
  })
}

// Keep irreversible steps inside the request: a model asked to clean up or
// fix something tends to go further than asked when it judges that more
// thorough (seen: asked to remove secrets from a repository, it rewrote and
// pruned the whole git history, destroying commits the user still needed).
// Such steps cannot be undone, so they need the request's say-so, not the
// model's judgement. Always on, as standing context at every step.
const SCOPE_TEXT = 'Keep to what the request asks for. Do not take an irreversible step the request does not ask for: rewriting or pruning version history (git filter-branch or filter-repo, rebase, reset --hard, reflog expire, gc --prune, deleting branches or tags), force-pushing, deleting files or data you did not create, or changing system or global settings. Where the request can be met by changing the current files, do only that and leave history and everything else as it was. If you believe such a step is needed, say so in your final answer instead of taking it.'

function scopeGuard (ctx) {
  ctx.inject(['systemPrompt'], (ctx) => {
    ctx.systemPrompt.context({ name: 'alcove:scope', order: 120, interpolate: false, text: () => SCOPE_TEXT })
  })
}

// Continue after the output limit: a turn that ends because one model
// response ran into the output token limit (dsh ends the turn there: kind
// "max-tokens") is handed back with a short nudge as a new turn, the way a
// user would type "continue". ALCOVE_MAX_TOKENS_CONTINUE is how many times in
// a row (0 or unset: off); a turn that ends any other way resets the count.
const CONTINUE_TEXT = 'Your previous response hit the output length limit before it finished, so nothing in it was carried out. Do not restate or redo that reasoning. Continue the task from where things stand: take the next concrete action now (a tool call), keeping any reasoning short.'

function maxTokensContinue (ctx) {
  const limit = Number.parseInt(process.env.ALCOVE_MAX_TOKENS_CONTINUE ?? '', 10)
  if (!(limit > 0)) return
  const streak = new Map() // session id -> consecutive continues
  ctx.on('session/event', (session, event) => {
    if (event?.type !== 'turn/end') return
    const id = session?.id
    if (typeof id !== 'string') return
    if (event.data?.reason?.kind !== 'max-tokens') { streak.delete(id); return }
    const n = streak.get(id) ?? 0
    if (n >= limit) { streak.delete(id); return }
    const agent = ctx.get('agents')?.get(id)
    if (!agent || agent.session !== session) return
    streak.set(id, n + 1)
    // turn/end is recorded while the driver still runs and has already
    // decided the inbox is empty; a followup sent now is queued but nothing
    // wakes the agent for it. Send it once the agent is idle.
    agent.whenIdle().then(() => {
      if (ctx.get('agents')?.get(id) !== agent || agent.session !== session) return
      agent.followup({
        id: randomUUID(),
        role: 'user',
        content: [{ type: 'text', text: CONTINUE_TEXT }],
        source: { kind: 'user' }
      })
    }, (err) => warn(`max-tokens continue: ${err?.message ?? err}`))
  })
}

// Check the work before finishing: when a turn that used tools is about to
// end with the model's final answer, hand it one more step that asks it to
// verify each requirement of the request with a concrete check, the way a
// reviewer would ask "did you test X?". Models tend to stop once the main
// path works and miss stated edge cases. ALCOVE_SELF_CHECK is the number of
// tool calls a turn needs before it is checked (0 or unset: off), and
// ALCOVE_SELF_CHECK_ROUNDS how many checks a turn gets (default 1): the
// second looks for what the first one's checks left out (or, with
// ALCOVE_SELF_CHECK_REVIEWER, hands an independent reviewer's report back).
// A turn is never checked after an output-limit stop.
// Every check ends by looking at what the work leaves behind: graders
// often check that nothing else was added or changed (a stray compiled
// binary beside a source file, an edited file outside the task), and the
// checks themselves create such files.
const LEAVE_BEHIND_TEXT = 'Last, look at what you leave behind. List what changed in the working directory and any other place the request names (git status, or a listing compared with how it was), and remove what you added that the request neither asks for nor needs in order to work: compiled programs, scratch scripts and inputs, logs, backups. Undo changes the work does not need, such as edits to files unrelated to the request. A server or process the request says should be running must still be running after your session ends, not only now: a process started from your shell can be killed with it when your commands finish, so start it detached (for example setsid nohup <command> > <log file> 2>&1 < /dev/null &), restarting it that way if it was not, and confirm it is listening or working from a fresh command.'
const SELF_CHECK_TEXTS = [
  'Before you finish, check your work against the request. Re-read the original request in full and list every explicit requirement, constraint and edge case it states (formats, error cases, precedence and override rules, file locations, exact outputs). Confirm each one with a concrete check that exercises it (run a test, a command or a short script), not by rereading your code. Where a result depends on where a boundary falls (where an inserted or removed stretch begins, where one field ends and the next starts) and the data let it fall in more than one place, run each of those checks with the boundary in each place, and make the result hold in all of them. Where the request leaves a detail open (how whitespace, case, an empty or malformed value is treated), search the existing code base and the libraries it already depends on for how they handle the same thing, and do the same. Where two of them handle it differently, follow the one for the same protocol or format the changed code handles (an HTTP parser for HTTP data rather than a mail parser, say), not one for a neighbouring format. Follow a standard only where they have no precedent. Fix anything that fails and re-run the checks. Name any test functions and helpers you added after your change, not after the feature in general, so the package still builds when someone else adds tests for the same feature (in Go, for one, a name declared twice in a package breaks the whole package). ' + LEAVE_BEHIND_TEXT + ' If everything already passes such checks, say so briefly and give your final answer.',
  'Review once more, as a reviewer who did not write this code. Your checks so far confirmed what the request says should happen; now look for what they left out. For each requirement, check the cases around it: inputs that must be rejected, ignored or left out of a result; every combination of the options and settings the request mentions, including the feature switched off; inputs written in another valid form than the one you tested (other whitespace, case, quoting, line endings, an empty or missing value). Where the result depends on where a boundary falls (where an inserted or removed stretch begins, where one field ends and the next starts) and the data let it fall in more than one place, check the result with the boundary in each of those places. If you built a program, script or function, it will be judged on inputs you have not seen: make a second, different input of the same kind as the sample (generate, build or compile one) and confirm the result is still right for it, checking it against an independent source such as a standard tool, not against your own code. Where the request leaves a detail open, do what the existing code base, and the libraries it already uses, do for the same thing, and where two of them differ, what the one for the same protocol or format the changed code handles does; only where they have no precedent, follow the governing standard (an RFC, the language or format specification) for this same protocol or format. Check that. Fix what fails, re-run the full test suite. ' + LEAVE_BEHIND_TEXT + ' Then give your final answer.'
]

// ALCOVE_SELF_CHECK_REVIEWER=1: the second check is done by a reviewer, a
// subagent in a fresh context that sees only the request and the working
// directory, not the conversation that produced the work. A model checking
// its own work tends to repeat the choices it made writing it; a reader
// without that history tests the request as written. Its report goes back
// to the agent as the second check's message. The reviewer starts with the
// first check and works while the agent runs it, so it costs the task
// little time of its own. At the second check the agent waits for the
// report; a reviewer still testing at the deadline is told to report what
// it has confirmed so far. If it fails or gives no report, the second check
// falls back to its plain text.
const REVIEW_REQUEST_TEXT = (request) => `Another agent has just finished work in the current working directory on the request quoted below. Review that work as an independent tester: you did not write it, and its own tests and summary are not evidence.

1. Read the request and list every requirement, and the cases around each one: inputs that must be rejected, ignored or left out; every combination of the options and settings it mentions, including the feature switched off; inputs in another valid form than the obvious one (other whitespace, case, quoting, line endings, an empty or missing value); where the result depends on where a boundary falls (where an inserted or removed stretch begins, where one field ends and the next starts) and the data let it fall in more than one place, each of those places. Where the request leaves a detail open, first find, by searching their source, what the existing code base and the libraries it already depends on do for the same thing, and where two of them differ, the one for the same protocol or format the changed code handles; only where they have no precedent, what the governing standard (an RFC, the language or format specification) for this same protocol or format says.
2. Look at the result (the changed files, the program, the output files) and test it against that list with checks you write and run yourself. A program, script or function will be judged on inputs nobody has seen: make new inputs of the same kind as any sample (generate, build or compile them) and compare the result with an independent source such as a standard tool. Where a boundary can fall in more than one place, run every check with it in each place: a requirement that holds in one place and fails in another is a problem to report. That source must follow the same standard the request is about: a tool for a neighbouring format (mail rather than HTTP, say) can follow different rules.
3. Do not change, fix, move or delete any file of the work, and do not commit. Keep your own test files in a new directory under /tmp and leave no files in the working directory.

Reply with a report of at most 400 words. Begin it with the line "Confirmed problems: N", N being the number of problems you confirmed by running a check (0 if none). Then give each problem you confirmed by running a check, with the input, the expected and actual result, and why that result is expected (cite the request, the standard or the existing code); then anything you could not check. A finding that rests only on one reading of a detail the request leaves open, with no precedent in the code base, is not a confirmed problem: list it apart as an open question. If you found no problem, say so.

<request>
${request}
</request>`
const REVIEW_RESULT_TEXT = (report) => `An independent reviewer, who saw only the request and your work, not this conversation, tested it and reported:

<review>
${report}
</review>

The reviewer can be wrong: weigh each finding against the request and confirm it by running the check yourself. Where the result holds under one reading of the request and fails under another, make it hold under every reading you can. Where two readings cannot both hold, keep the one the existing code base and the libraries it uses already follow for the same thing (where they differ, the one for the same protocol or format the changed code handles), and change your choice only when that precedent supports the reviewer. Fix the real problems and re-run the full test suite. ${LEAVE_BEHIND_TEXT} Then give your final answer.`
const REVIEW_TIMEOUT_MS = 20 * 60_000
const REVIEW_WRAP_UP_MS = 2 * 60_000
const REVIEW_WRAP_UP_TEXT = 'Time is up: stop testing and reply now with your report, in the form asked for, of the problems you have confirmed so far and what you could not check.'
const reviewers = new Set() // ids of reviewer agents

// The count a report opens with ("Confirmed problems: N"); undefined when the
// reviewer did not give one.
function confirmedProblems (report) {
  const m = /confirmed problems:\s*(\d+)/i.exec(report ?? '')
  return m ? Number(m[1]) : undefined
}

// ALCOVE_SELF_CHECK_EXTRA=N: a task with a long time budget gets up to N
// more reviews after the regular checks. The regular checks take some tens
// of minutes whatever the budget, so on a task allowed hours the agent
// finished with most of its time unused while its work still missed a case
// a fresh tester could find (seen on FrontierHarness v1.1's 8-hour tasks:
// done in under 15% of the budget, failing by one hidden test). Each extra
// round starts a new reviewer, which waits for no other work, and hands its
// report back as the second check's does. Rounds stop when a reviewer
// confirms no problem, when half the budget is spent, or after N rounds.
const EXTRA_MIN_BUDGET_MS = 60 * 60_000
const EXTRA_MIN_LEFT = 0.5

function extraReviewAllowed (session) {
  const b = budgets.get(session)
  return !!b && b.ms >= EXTRA_MIN_BUDGET_MS && budgetLeft(session) >= EXTRA_MIN_LEFT
}

// Start a reviewer on the request; undefined if none could be started.
async function startReview (ctx, agent, request, signal) {
  const subagents = ctx.get('subagents')
  if (!subagents) return
  try {
    const activation = await subagents.startActivation({
      provider: 'spawn',
      label: 'Independent review',
      request: { prompt: [{ type: 'text', text: REVIEW_REQUEST_TEXT(request) }], parent: agent },
      signal,
      delivery: 'caller'
    })
    reviewers.add(activation.childId)
    return { activation, start: Date.now() }
  } catch (err) {
    warn(`review: ${err?.message ?? err}`)
  }
}

// The reviewer's report, waiting at most until REVIEW_TIMEOUT_MS after its
// start and half the time the budget has left (so the agent has time to
// act on it), then asking it to wrap up. Disposes the reviewer.
async function collectReview (ctx, agent, review, signal) {
  const { activation } = review
  const wait = (ms) => {
    let timer
    const timeout = new Promise((resolve) => { timer = setTimeout(resolve, Math.max(0, ms)) })
    const aborted = new Promise((resolve) => signal?.addEventListener('abort', resolve, { once: true }))
    return Promise.race([activation.result, timeout, aborted]).finally(() => clearTimeout(timer))
  }
  try {
    const b = budgets.get(agent.session)
    const half = b ? (b.ms - (Date.now() - b.start)) / 2 : Infinity
    let result = await wait(Math.min(REVIEW_TIMEOUT_MS - (Date.now() - review.start), half - REVIEW_WRAP_UP_MS))
    if (!result && !signal?.aborted) {
      const child = ctx.get('agents')?.get(activation.childId)
      if (child) {
        child.steer({ id: randomUUID(), role: 'user', content: [{ type: 'text', text: REVIEW_WRAP_UP_TEXT }], source: { kind: 'user' } })
        result = await wait(REVIEW_WRAP_UP_MS)
      }
    }
    if (!result) { warn(`review: no report after ${Math.round((Date.now() - review.start) / 1000)} s`); return }
    const report = (result.output ?? []).filter((b) => b?.type === 'text').map((b) => b.text).join('\n').trim()
    if (result.stopReason !== undefined && result.stopReason !== 'completed') warn(`review: reviewer ended ${result.stopReason}`)
    return report || undefined
  } catch (err) {
    warn(`review: ${err?.message ?? err}`)
  } finally {
    dropReview(review)
  }
}

function dropReview (review) {
  if (!review) return
  reviewers.delete(review.activation.childId)
  review.activation.dispose().catch(() => {})
}

function finishReason (data) {
  const stream = Array.isArray(data?.stream) ? data.stream : []
  for (let i = stream.length - 1; i >= 0; i--) {
    const kind = stream[i]?.chunk?.type === 'finish' ? stream[i].chunk.reason?.kind : undefined
    if (kind) return kind
  }
  return data?.message?.source?.replayState?.response?.stopReason === 'length' ? 'max-tokens' : undefined
}

function selfCheck (ctx) {
  const minTools = Number.parseInt(process.env.ALCOVE_SELF_CHECK ?? '', 10)
  if (!(minTools > 0)) return
  const rounds = Math.min(SELF_CHECK_TEXTS.length, Math.max(1, Number.parseInt(process.env.ALCOVE_SELF_CHECK_ROUNDS ?? '1', 10) || 1))
  const turns = new WeakMap() // session -> { turn, tools, truncated, checked }
  ctx.on('session/event', (session, event) => {
    const turn = event?.data?.turn
    if (event?.type === 'turn/start') {
      // A turn that follows one cut off at the output limit (the
      // max-tokens continue) carries on the same work, so it keeps that
      // turn's tool count and checks: otherwise a long task whose first turn
      // was cut would end its second turn unchecked.
      const prev = turns.get(session)
      const carried = prev?.truncated ? prev : undefined
      if (!carried) dropReview(prev?.review)
      turns.set(session, { turn, step: 0, tools: carried?.tools ?? 0, truncated: false, checked: carried?.checked ?? 0, request: carried?.request, review: carried?.review })
    }
    const s = turns.get(session)
    // The request a reviewer checks against: the first message the user
    // sent in the turn (user/message carries no turn number).
    if (event?.type === 'user/message' && s && s.request === undefined && event.data?.source?.kind === 'user') {
      s.request = (event.data.content ?? []).filter((b) => b?.type === 'text').map((b) => b.text).join('\n')
      return
    }
    if (!s || s.turn !== turn) return
    if (event.type === 'tool/result') s.tools++
    else if (event.type === 'step/start') s.step = event.data?.step ?? s.step
    else if (event.type === 'assistant/message') s.truncated = finishReason(event.data) === 'max-tokens'
  })
  const reviewer = process.env.ALCOVE_SELF_CHECK_REVIEWER === '1'
  const extra = reviewer ? Math.max(0, Number.parseInt(process.env.ALCOVE_SELF_CHECK_EXTRA ?? '0', 10) || 0) : 0
  ctx.on('agent/turn-stopping', async ({ agent, turn, signal }) => {
    const s = agent?.session && turns.get(agent.session)
    if (!s || s.turn !== turn || s.truncated) return
    // A reviewer's own work is not checked again.
    if (reviewers.has(agent.id)) return
    // A check started this close to the time budget's end would be cut off
    // halfway, possibly with files half rewritten. The turn ends unchecked,
    // and a reviewer still working is stopped.
    if (s.tools < minTools || budgetLeft(agent.session) < 0.1) {
      dropReview(s.review); s.review = undefined
      return
    }
    if (s.checked >= rounds) {
      // Extra rounds follow only a review that confirmed something (or gave
      // no count), so a clean report ends them.
      if (s.checked >= rounds + extra || s.found === 0 || !s.request || !extraReviewAllowed(agent.session)) return
      s.checked++
      const review = await startReview(ctx, agent, s.request, signal)
      const report = review && await collectReview(ctx, agent, review, signal)
      if (!report) return
      s.found = confirmedProblems(report)
      if (s.found === 0) return
      markDeepStep(agent.session, turn, s.step + 1)
      agent.inject({ id: randomUUID(), role: 'user', content: [{ type: 'text', text: REVIEW_RESULT_TEXT(report) }], source: { kind: 'user' } })
      return
    }
    const round = s.checked++
    let text = SELF_CHECK_TEXTS[round]
    if (reviewer && round === 0 && rounds > 1 && s.request) {
      s.review = await startReview(ctx, agent, s.request, signal)
    } else if (round === 1 && s.review) {
      const report = await collectReview(ctx, agent, s.review, signal)
      s.review = undefined
      if (report) { text = REVIEW_RESULT_TEXT(report); s.found = confirmedProblems(report) }
    }
    markDeepStep(agent.session, turn, s.step + 1)
    agent.inject({
      id: randomUUID(),
      role: 'user',
      content: [{ type: 'text', text }],
      source: { kind: 'user' }
    })
  })
}

// Keep to a time budget: ALCOVE_TIME_BUDGET is the wall-clock seconds a
// session's work may take (an automation run, a scheduled task) before it is
// cut off. The model cannot see the clock, so a long task tends to keep
// exploring until the cut and leave nothing behind. Halfway through, the
// model is told how much time remains and to put its current best result
// where the request asks for it before refining; near the end, to finish.
// The clock starts at the session's first turn, so a turn the runtime adds
// on its own (an output-limit continue) shares the budget.
const budgets = new WeakMap() // session -> { start, ms, warned }

function budgetLeft (session) {
  const b = budgets.get(session)
  return b ? Math.max(0, 1 - (Date.now() - b.start) / b.ms) : 1
}

const minutesLeft = (b) => {
  const m = Math.max(1, Math.round((b.ms - (Date.now() - b.start)) / 60_000))
  return m === 1 ? 'about 1 minute remains' : `about ${m} minutes remain`
}
const BUDGET_STAGES = [
  {
    left: 0.5,
    text: (m) => `Time check: ${m} in this task's time budget; work still running when it runs out is cut off and lost. If the result the request asks for (a file, an answer, a commit) does not exist yet, produce your current best version of it now, then use the remaining time to verify and improve it.`
  },
  {
    left: 0.15,
    text: (m) => `Time check: ${m} before this task is cut off. Stop exploring: make sure the requested result is in place in its final form now, then give your final answer.`
  }
]

function timeBudget (ctx) {
  const seconds = Number(process.env.ALCOVE_TIME_BUDGET)
  if (!(seconds > 0)) return
  ctx.on('session/event', (session, event) => {
    if (event?.type === 'turn/start' && !budgets.has(session)) {
      budgets.set(session, { start: Date.now(), ms: seconds * 1000, warned: 0 })
    }
  })
  // The note rides on a tool result (as dsh's repeat-tool reminder does), so
  // the model reads it in the very next step. agent.inject cannot be used
  // here: from a session event it would reenter the session's append.
  ctx.on('tools/post-execute', async (exec, _result, next) => {
    const downstream = await next()
    const session = exec?.agent?.session
    const b = session && budgets.get(session)
    if (!b) return downstream
    const left = budgetLeft(session)
    let stage
    while (b.warned < BUDGET_STAGES.length && left <= BUDGET_STAGES[b.warned].left) stage = BUDGET_STAGES[b.warned++]
    if (!stage) return downstream
    const note = Object.freeze({
      id: randomUUID(),
      role: 'user',
      content: [{ type: 'text', text: stage.text(minutesLeft(b)) }],
      source: { kind: 'alcove-time-budget', form: 'notice', summary: 'time budget' }
    })
    return { ...downstream, additionalContexts: [note, ...downstream.additionalContexts ?? []] }
  })
}

// Think harder where it pays: ALCOVE_PLAN_EFFORT names a reasoning effort
// (e.g. high) for the steps that decide the work, the first step of a turn,
// where the model reads the request and plans, and the self-check step;
// every other step (read output, run the next command) keeps the runtime's
// effort. Deep thinking on every step makes a long task slow enough to miss
// its time limit, and little thinking on the plan misses requirements.
const deepSteps = new WeakMap() // session -> Set of "turn:step"

function markDeepStep (session, turn, step) {
  if (!process.env.ALCOVE_PLAN_EFFORT) return
  let set = deepSteps.get(session)
  if (!set) deepSteps.set(session, set = new Set())
  set.add(`${turn}:${step}`)
}

function planEffort (ctx) {
  const effort = process.env.ALCOVE_PLAN_EFFORT
  if (!effort) return
  // dsh seeds each request with the previous one's config, so a step after a
  // deep one would keep the plan effort: remember the runtime's own effort
  // from the first request of the session and put it back.
  const base = new WeakMap() // session -> { effort }
  ctx.on('agent/request', async ({ agent, turn, step }, next) => {
    const config = await next()
    const session = agent?.session
    if (!session) return config
    if (!base.has(session)) base.set(session, { effort: config.reasoningEffort })
    const deep = step === 1 || deepSteps.get(session)?.has(`${turn}:${step}`)
    const { reasoningEffort: _, ...rest } = config
    const want = deep ? effort : base.get(session).effort
    return want === undefined ? rest : { ...rest, reasoningEffort: want }
  })
}

// Repair DSML leaks in tool arguments. DeepSeek models write tool calls in
// their own DSML markup; an OpenAI-compatible server (seen with Vertex AI's
// DeepSeek) that misses a variant such as `</｜DSML｜ parameter>` passes the
// raw markup through, so the first string argument swallows the rest of the
// call: `cd /app && make</｜DSML｜ parameter>\n<parameter name="description">…`.
// The command then fails on syntax the model never meant. Before the tool
// runs, cut such a value at the first DSML tag and restore the parameters
// that followed it. Only strings where the markup is out of place are
// touched (leakedDsml): DSML written on purpose, such as a parser fixture or
// protocol notes, is a value like any other.
const DSML = '｜DSML｜'
// Any tag that opens with the marker, however mangled (`</｜DSML｜ parameter`
// with no `>`, `parameter_placeholder>`, `parameter file_path=`).
const DSML_TAG = /<\/?\s*｜DSML｜/
// A tag's opening, its slash and its name (`parameter_placeholder` is a
// mangled `parameter`).
const DSML_TAGS = /<(\/?)\s*｜DSML｜\s*([A-Za-z_]*)/g

// leakedDsml reports whether a value carries the rest of its own call: a
// closing tag with nothing open before it (the value's parameter ending), or
// a parameter opening outside any invoke (the call's next parameter). Markup
// written as content nests parameter in invoke in function_calls and closes
// what it opens.
export function leakedDsml (value) {
  const open = []
  for (const [, close, raw] of value.matchAll(DSML_TAGS)) {
    const name = raw.startsWith('parameter') ? 'parameter' : raw
    if (close) {
      if (open.at(-1) !== name) return true
      open.pop()
    } else {
      if (name === 'parameter' && !open.includes('invoke')) return true
      open.push(name)
    }
  }
  return false
}

const DSML_PARAM = /<\s*(?:｜DSML｜\s*)?parameter\s+name="([^"]+)"([^>]*)>([\s\S]*?)(?=<\/?\s*(?:｜DSML｜\s*)?(?:parameter|invoke|function_calls)\b|$)/g

export function repairDsmlArguments (args) {
  if (args === null || typeof args !== 'object' || Array.isArray(args)) return undefined
  let out
  for (const [key, value] of Object.entries(args)) {
    if (typeof value !== 'string' || !value.includes(DSML) || !leakedDsml(value)) continue
    const cut = value.search(DSML_TAG)
    if (cut < 0) continue
    out ??= { ...args }
    out[key] = value.slice(0, cut)
    for (const m of value.slice(cut).matchAll(DSML_PARAM)) {
      const [, name, attrs, raw] = m
      if (name in out) continue
      const text = raw.replace(/\s+$/, '')
      if (/string\s*=\s*"false"/.test(attrs)) {
        try { out[name] = JSON.parse(text); continue } catch {}
      }
      out[name] = text
    }
  }
  return out
}

function dsmlRepair (ctx) {
  ctx.on('tools/execute', async (exec, next) => {
    const repaired = repairDsmlArguments(exec.arguments)
    if (repaired) exec.arguments = repaired
    return next()
  })
  // Repairing only what runs leaves the raw call in the conversation, and the
  // model copies what it sees there: on the bench one leak grew into 150K
  // characters of DSML tags inside a single argument, and every response
  // after it ran to the output limit producing more. So the finished
  // tool-call block is repaired as it leaves the model's stream, before the
  // agent records it; dsh has no hook there, so this wraps the llm service's
  // prepared call.
  ctx.inject(['llm'], (ctx) => {
    const llm = ctx.llm
    const original = llm.prepareCall
    if (typeof original !== 'function') return
    const patched = async function (...args) {
      const call = await original.apply(this, args)
      if (!call || typeof call.stream !== 'function') return call
      return Object.freeze({ ...call, stream: (options) => repairToolCallStream(call.stream(options)) })
    }
    llm.prepareCall = patched
    ctx.on('dispose', () => { if (llm.prepareCall === patched) llm.prepareCall = original })
  })
}

async function * repairToolCallStream (stream) {
  for await (const chunk of stream) {
    const block = chunk?.type === 'block-end' && chunk.block?.type === 'tool-call' ? chunk.block : undefined
    if (!block) { yield chunk; continue }
    const repaired = repairToolCallArguments(block.arguments)
    const args = repaired ?? block.arguments
    const name = misusedReadImage(block.name, args) ? 'read' : block.name
    yield repaired === undefined && name === block.name ? chunk : { ...chunk, block: { ...block, name, arguments: args } }
  }
}

// DeepSeek models often pick `read_image` where they mean `read`, for source
// files and logs (6 to 26 times a task on the bench). Each such call fails on
// the file's extension and costs a step, and the model starts reasoning about
// its own tool choice. A read_image call on a file whose extension is not an
// image format read_image takes becomes the `read` call it was meant to be,
// before the agent records it.
const READ_IMAGE_EXTENSIONS = new Set(['.png', '.jpg', '.jpeg', '.webp', '.gif'])

function misusedReadImage (name, args) {
  if (name !== 'read_image') return false
  let parsed = args
  if (typeof args === 'string') { try { parsed = JSON.parse(args) } catch { return false } }
  const path = parsed?.file_path
  if (typeof path !== 'string') return false
  const ext = extname(path).toLowerCase()
  return ext !== '' && !READ_IMAGE_EXTENSIONS.has(ext)
}

// Arguments arrive as an object, or as the raw JSON text when they did not parse.
function repairToolCallArguments (args) {
  if (typeof args !== 'string') return repairDsmlArguments(args)
  if (!args.includes(DSML)) return undefined
  let parsed
  try { parsed = JSON.parse(args) } catch { return undefined }
  const repaired = repairDsmlArguments(parsed)
  return repaired && JSON.stringify(repaired)
}

// Refuse a `write` whose content is empty. DeepSeek models, after long
// reasoning, sometimes emit the write call with the content left out (seen in
// about one in ten writes on the bench); the tool then creates an empty file,
// reports success, and the model loses steps working out what happened. An
// empty file on purpose is rare and still possible through the shell.
const EMPTY_WRITE = 'Nothing was written: this write call arrived with empty content. Send the write again with the complete file content in the content argument. To create an empty file on purpose, use a shell command such as `: > path`.'

function emptyWriteGuard (ctx) {
  ctx.on('tools/execute', async (exec, next) => {
    const content = exec.name === 'write' ? exec.arguments?.content : undefined
    if (exec.name !== 'write' || (typeof content === 'string' && content !== '')) return next()
    return { isError: true, error: { message: EMPTY_WRITE }, content: [{ type: 'text', text: `Error: ${EMPTY_WRITE}` }] }
  })
}

// Live output for the conversation view. dsh's SDK server sends only durable
// session events: the model's reply arrives whole in assistant/message (the
// reply's chunks go to agent/assistant-stream, which only dsh's own web
// server forwards), what a running command prints not at all (bash and pwsh
// register each call as a job, and the job's output ring is read only by
// the tool when it returns), and a compaction's summary whole in
// compaction/summary (one llm.stream call). All three go to the host as
// session.live deltas, best effort: the session events stay the record, and
// a delta the host misses only shows later. A job is tied to its call by the
// job/registered event dsh records for it; its output events say only that
// the ring grew, so each one reads from where the last stopped.
const LIVE_MAX = 16 << 10 // a delta larger than this is sent as its tail
const LIVE_CHUNKS = new Set(['text-delta', 'reasoning-delta', 'tool-call-delta'])

function liveOutput (ctx, peerP) {
  let peer
  peerP.then((p) => { peer = p })
  const send = (params) => {
    if (peer && !peer.closed) peer.notify('session.live', params)
  }
  const attempts = new Map() // reply attempt id -> { sessionId, turn, step }
  ctx.on('agent/assistant-stream', ({ agent, frame }) => {
    if (frame?.type === 'start') {
      const sessionId = agent?.session?.id
      if (sessionId !== undefined) attempts.set(frame.attemptId, { sessionId: String(sessionId), turn: frame.turn, step: frame.step })
      return
    }
    const a = attempts.get(frame?.attemptId)
    if (!a) return
    const at = { sessionId: a.sessionId, turn: a.turn, step: a.step, attempt: frame.attemptId }
    if (frame.type === 'end') {
      attempts.delete(frame.attemptId)
      if (frame.outcome?.kind === 'abandoned') send({ kind: 'abandon', ...at })
    } else if (frame.type === 'chunk' && LIVE_CHUNKS.has(frame.chunk?.type)) {
      send({ kind: 'chunk', ...at, chunk: frame.chunk })
    }
  }, { global: true })
  const jobs = new Map() // job id -> { sessionId, callId, owner, next }
  // dsh records compaction/start just before it calls the model for the
  // summary, and the call does not carry the compaction's id: each summary
  // delta is sent with the session's open compaction, so the host can drop
  // one that trails the compaction's end without dropping the next one's.
  const compacting = new Map() // session id -> open compaction id
  let calls = 0
  ctx.on('session/event', (session, event) => {
    if (event?.type === 'compaction/start') compacting.set(String(session.id), event.data?.compactionId)
    else if (event?.type === 'compaction/end') compacting.delete(String(session.id))
    if (event?.type !== 'job/registered') return
    const d = event.data
    const callId = d?.toolCall?.callId
    if (typeof d?.jobId !== 'string' || typeof callId !== 'string' || d.owner === undefined) return
    jobs.set(d.jobId, { sessionId: String(session.id), callId, owner: d.owner, next: 0 })
  })
  ctx.inject(['jobs'], (ctx) => {
    const drain = (id) => {
      const job = jobs.get(id)
      if (!job) return
      let read
      try { read = ctx.jobs.readAt(id, job.next, job.owner) } catch { jobs.delete(id); return }
      job.next = read.next
      let text = read.chunks.map((c) => c.text).join('')
      let lossy = read.lossy || read.chunks.some((c) => c.gapBefore)
      if (text.length > LIVE_MAX) { text = text.slice(-LIVE_MAX); lossy = true }
      if (text) send({ sessionId: job.sessionId, kind: 'tool-output', callId: job.callId, text, ...(lossy ? { lossy } : {}) })
    }
    ctx.jobs.events.subscribe({}, (event) => {
      if (event.type === 'output') drain(event.id)
      else if (event.type === 'settled' || event.type === 'removed') {
        const id = event.job?.id ?? event.id
        drain(id)
        jobs.delete(id)
      }
    })
  })
  ctx.on('llm/stream', (options, next) => {
    const stream = next()
    if (options?.purpose !== 'compaction' || options.sessionId === undefined) return stream
    const sessionId = String(options.sessionId)
    const compactionId = compacting.get(sessionId)
    if (typeof compactionId !== 'string') return stream
    return teeCompaction(stream, { sessionId, kind: 'compaction', compactionId, call: ++calls }, send)
  }, { global: true })
}

async function * teeCompaction (stream, at, send) {
  for await (const chunk of await stream) {
    if (chunk?.type === 'text-delta' && chunk.text) send({ ...at, text: chunk.text })
    yield chunk
  }
}

export function apply (ctx) {
  personalProfile(ctx)
  scopeGuard(ctx)
  maxTokensContinue(ctx)
  selfCheck(ctx)
  timeBudget(ctx)
  planEffort(ctx)
  dsmlRepair(ctx)
  emptyWriteGuard(ctx)
  const endpoint = process.env.ALCOVE_BRIDGE_HOST
  const token = process.env.ALCOVE_BRIDGE_HOST_TOKEN
  if (!endpoint || !token) return
  // Connect once per process; the token is single-use, so drop it from the
  // environment that tools and child processes inherit.
  delete process.env.ALCOVE_BRIDGE_HOST_TOKEN
  const peerP = connect(endpoint, token, async (method, params) => {
    if (method === 'session.cancel') {
      const agent = typeof params.sessionId === 'string' ? ctx.get('agents')?.get(params.sessionId) : undefined
      if (!agent) return { cancelled: false }
      await agent.cancel({ kind: 'user' }, { keepInbox: false })
      return { cancelled: true }
    }
    throw Object.assign(new Error(`unsupported: ${method}`), { code: -32021 })
  })
  liveOutput(ctx, peerP)

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
