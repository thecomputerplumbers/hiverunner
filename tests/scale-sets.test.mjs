import assert from "node:assert/strict"
import { test } from "node:test"
import {
  createScaleSetClient,
  ScaleSetError,
} from "../packages/github/src/scale-sets.ts"

function fake() {
  const calls = []
  const session = {
    sessionId: "session-1",
    messageQueueUrl: "https://jobs.actions.githubusercontent.com/queue",
    messageQueueAccessToken: "session-secret",
  }
  let created = false
  const send = async (raw, init) => {
    const url = new URL(raw)
    assert.equal(init.redirect, "manual")
    calls.push({ url, init })
    if (url.pathname.endsWith("registration-token")) {
      assert.equal(init.headers.Authorization, "Bearer installation-secret")
      return Response.json({ token: "registration-secret" })
    }
    if (url.pathname === "/actions/runner-registration") {
      assert.equal(init.headers.Authorization, "RemoteAuth registration-secret")
      return Response.json({
        url: "https://jobs.actions.githubusercontent.com/tenant",
        token: "admin-secret",
      })
    }
    if (url.pathname.endsWith("runnergroups/"))
      return Response.json({ value: [{ id: 1 }] })
    if (url.pathname.endsWith("runnerscalesets")) {
      if (init.method === "POST") {
        created = true
        assert.equal(JSON.parse(init.body).labels[0].name, "hive")
        return Response.json({ id: 6, name: "hive", runnerGroupId: 1 })
      }
      return Response.json({
        value: created ? [{ id: 6, name: "hive", runnerGroupId: 1 }] : [],
      })
    }
    if (url.pathname.endsWith("sessions")) return Response.json(session)
    if (url.pathname.endsWith("sessions/session-1"))
      return new Response(null, { status: 204 })
    if (url.pathname.endsWith("acquirejobs")) {
      assert.equal(init.headers.Authorization, "Bearer session-secret")
      return Response.json({ value: JSON.parse(init.body) })
    }
    if (url.pathname.endsWith("generatejitconfig"))
      return Response.json({
        runner: { id: 9, name: "hw-m-a" },
        encodedJITConfig: "one-time-secret",
      })
    if (url.pathname === "/queue") {
      assert.equal(init.headers.Authorization, "Bearer session-secret")
      assert.equal(init.headers["X-ScaleSetMaxCapacity"], "1")
      return Response.json({
        messageId: 12,
        statistics: { totalAssignedJobs: 1, totalRunningJobs: 0 },
        body: JSON.stringify([
          {
            messageType: "JobAvailable",
            runnerRequestId: 100,
            ownerName: "acme",
            repositoryName: "app",
          },
        ]),
      })
    }
    if (url.pathname === "/queue/12") return new Response(null, { status: 204 })
    throw new Error(`Unexpected fake request: ${url.pathname}`)
  }
  return {
    calls,
    client: createScaleSetClient({
      organization: "acme",
      installationToken: async () => "installation-secret",
      fetch: send,
    }),
  }
}

test("scale sets reuse registration and send the correct credential to each service", async () => {
  const { client, calls } = fake()
  assert.equal((await client.ensureScaleSet("hive")).id, 6)
  assert.equal((await client.ensureScaleSet("hive")).id, 6)
  assert.equal(
    calls.filter((c) => c.url.pathname.endsWith("registration-token")).length,
    1
  )
  assert.equal(
    calls.filter(
      (c) =>
        c.url.pathname.endsWith("runnerscalesets") && c.init.method === "POST"
    ).length,
    1
  )
  const session = await client.openSession(6, "owner")
  const message = await client.poll(session, 0, 1)
  assert.equal(message.jobs[0].runnerRequestId, 100)
  assert.deepEqual(await client.acquire(6, session, [100]), [100])
  assert.equal(
    (await client.jit(6, "hw-m-a")).encodedJITConfig,
    "one-time-secret"
  )
  await client.acknowledge(session, 12)
  await client.closeSession(6, session)
})

test("service credentials cannot be sent to URLs outside the Actions service", async () => {
  let sent = 0
  const client = createScaleSetClient({
    organization: "acme",
    installationToken: async () => "x",
    fetch: async () => {
      sent++
      throw new Error("Must not send")
    },
  })
  for (const url of [
    "http://jobs.actions.githubusercontent.com/queue",
    "https://actions.githubusercontent.com.evil.test/queue",
    "https://evil.test/queue",
    "https://user:pass@jobs.actions.githubusercontent.com/queue",
  ]) {
    await assert.rejects(
      client.poll(
        {
          sessionId: "x",
          messageQueueUrl: url,
          messageQueueAccessToken: "secret",
        },
        0,
        1
      ),
      /Untrusted/
    )
  }
  assert.equal(sent, 0)
})

test("idle long polls are empty; errors never include echoed credentials", async () => {
  const session = {
    sessionId: "x",
    messageQueueUrl: "https://jobs.actions.githubusercontent.com/queue",
    messageQueueAccessToken: "secret",
  }
  const client = createScaleSetClient({
    organization: "acme",
    installationToken: async () => "x",
    fetch: async () => new Response(null, { status: 202 }),
  })
  assert.equal(await client.poll(session, 1, 0), null)
  const denied = createScaleSetClient({
    organization: "acme",
    installationToken: async () => "x",
    fetch: async () => new Response("secret", { status: 401 }),
  })
  await assert.rejects(
    denied.poll(session, 1, 1),
    (error) =>
      error instanceof ScaleSetError &&
      error.status === 401 &&
      !error.message.includes("secret")
  )
})
