import { AsyncLocalStorage } from "node:async_hooks"
import { env } from "cloudflare:workers"
import { publicError } from "./errors"
const requests = new AsyncLocalStorage<{ requestId: string }>()
export const requestId = () => requests.getStore()?.requestId
// Allowlisted fields only: never serialize arbitrary errors, headers, URLs or payloads.
export function logEvent(
  event: string,
  fields: {
    status?: number
    durationMs?: number
    code?: string
    operation?: string
  } = {}
) {
  console.log(
    JSON.stringify({
      event,
      requestId: requestId(),
      status: fields.status,
      durationMs: fields.durationMs,
      code: fields.code,
      operation: fields.operation,
    })
  )
}
export function logError(error: unknown, operation: string) {
  logEvent("operation.failed", { operation, code: publicError(error).code })
}
// Work around Wrangler 4.147.0's local proxy losing early POST rejections.
// Production requests retain their immediate response; remove this workaround
// when the local proxy is fixed. Vinext transfers the entry's body, so guards
// must also call this with the actual routed Request before rejecting it.
// A 2026-10-04 deployed-Worker probe saw 0 failures in 300 early POST rejections.
export async function discardUnreadBody(request: Request) {
  if (!/^https?:\/\/(localhost|127\.0\.0\.1)(:\d+)?$/.test(env.APP_URL ?? ""))
    return
  if (request.body && !request.bodyUsed && !request.body.locked) {
    // Discard chunks without buffering the payload or logging its contents.
    await request.body.pipeTo(new WritableStream({ write() {} })).catch(() => {
      // A disconnected sender must not change the chosen error response.
    })
  }
}
export async function observeRequest(
  request: Request,
  handle: () => Promise<Response>
) {
  const id = crypto.randomUUID(),
    start = performance.now()
  return requests.run({ requestId: id }, async () => {
    let response: Response
    try {
      response = await handle()
    } catch (error) {
      logError(error, "request")
      response = Response.json(
        { ...publicError(error), requestId: id },
        { status: 500 }
      )
    }
    if (response.status >= 400) await discardUnreadBody(request)
    const headers = new Headers(response.headers)
    headers.set("X-Request-ID", id)
    logEvent("request.completed", {
      status: response.status,
      durationMs: Math.round(performance.now() - start),
    })
    // Preserve Workers-specific upgrade responses.
    if (response.status === 101) return response
    return new Response(response.body, {
      status: response.status,
      statusText: response.statusText,
      headers,
    })
  })
}
