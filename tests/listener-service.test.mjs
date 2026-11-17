import assert from "node:assert/strict"
import { test } from "node:test"
import {
  activateListeners,
  listenerWaiting,
} from "../apps/coordinator/src/listener-service.ts"

const env = {
  LISTENER_URL: "https://listener.example.test",
  LISTENER_API_TOKEN: "fixture",
}

test("listener activation aborts a stalled request after the configured deadline", async (t) => {
  const controller = new AbortController()
  t.mock.method(AbortSignal, "timeout", (ms) => {
    assert.equal(ms, 5000)
    return controller.signal
  })
  t.mock.method(
    globalThis,
    "fetch",
    (_url, init) =>
      new Promise((_resolve, reject) => {
        init.signal.addEventListener(
          "abort",
          () => reject(init.signal.reason),
          { once: true }
        )
      })
  )
  const activation = activateListeners(env, "a", ["hive-4cpu"])
  controller.abort(new DOMException("deadline", "TimeoutError"))
  await assert.rejects(activation, { name: "TimeoutError" })
})

test("activation releases its response body and failures propagate for durable retry", async (t) => {
  let cancelled = false
  t.mock.method(
    globalThis,
    "fetch",
    async () =>
      new Response(
        new ReadableStream({
          cancel() {
            cancelled = true
          },
        })
      )
  )
  await activateListeners(env, "a", ["hive-4cpu"])
  assert.equal(cancelled, true)
  t.mock.method(
    globalThis,
    "fetch",
    async () => new Response(null, { status: 503 })
  )
  await assert.rejects(activateListeners(env, "a", ["hive-4cpu"]), /503/)
})

test("waiting requests retain their bounded signal through body consumption", async (t) => {
  t.mock.method(globalThis, "fetch", async (_url, init) => {
    assert.ok(init.signal instanceof AbortSignal)
    return Response.json({ waiting: 2 })
  })
  assert.equal(await listenerWaiting(env, "a:hive-4cpu"), 2)
})
