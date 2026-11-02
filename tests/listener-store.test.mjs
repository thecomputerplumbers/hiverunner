// The rules both listener stores keep. Set LISTENER_TEST_DATABASE_URL to hold
// the Postgres store to them too; it uses a table of its own making, so point
// it at a scratch database.
import assert from "node:assert/strict"
import { test } from "node:test"
import {
  LeaseLost,
  memoryStore,
  postgresStore,
} from "../apps/listener/src/store.ts"

const settings = { organizationId: "org", label: "hive", vcpus: 2 }
const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

const stores = { memory: async () => memoryStore() }
if (process.env.LISTENER_TEST_DATABASE_URL)
  stores.postgres = () => postgresStore(process.env.LISTENER_TEST_DATABASE_URL)

for (const [kind, open] of Object.entries(stores)) {
  const unique = () => `org:${kind}-${crypto.randomUUID()}`
  const due = async (store, pod, name, lease = 60000) => {
    for (let i = 0; i < 40; i++) {
      if ((await store.claim(pod, [], lease)).includes(name)) return true
      await wait(100)
    }
    return false
  }

  test(`${kind}: only one pod holds a listener, and writes are fenced`, async () => {
    const store = await open()
    const name = unique()
    await store.activate(name, settings)
    assert.equal(await due(store, "a", name), true)
    assert.equal((await store.claim("b", [], 60000)).includes(name), false)
    await store.storage(name, "a").put("state", { cursor: 1 })
    await assert.rejects(
      store.storage(name, "b").put("state", { cursor: 2 }),
      LeaseLost
    )
    assert.deepEqual(await store.read(name, "state"), { cursor: 1 })
    await store.close()
  })

  test(`${kind}: a pod that dies mid-tick is taken over from its last save`, async () => {
    const store = await open()
    const name = unique()
    await store.activate(name, settings)
    assert.equal(await due(store, "a", name, 50), true)
    await store.storage(name, "a").put("state", { cursor: 3 })
    await wait(100)
    assert.equal(await due(store, "b", name), true)
    assert.deepEqual(await store.storage(name, "b").get("state"), { cursor: 3 })
    await assert.rejects(store.storage(name, "a").setAlarm(0), LeaseLost)
    await store.close()
  })

  test(`${kind}: a finished tick with no alarm sleeps until activated`, async () => {
    const store = await open()
    const name = unique()
    await store.activate(name, settings)
    assert.equal(await due(store, "a", name), true)
    await store.finish(name, "a")
    await wait(1200)
    assert.equal((await store.claim("a", [], 60000)).includes(name), false)
    await store.activate(name, settings)
    assert.equal(await due(store, "a", name), true)
    await store.close()
  })

  test(`${kind}: activating never changes a listener's identity`, async () => {
    const store = await open()
    const name = unique()
    await store.activate(name, settings)
    await assert.rejects(
      store.activate(name, { ...settings, label: "other" }),
      /identity mismatch/
    )
    await store.close()
  })

  test(`${kind}: a handover wins over a tick already running`, async () => {
    const store = await open()
    const name = unique()
    await store.activate(name, settings)
    assert.equal(await due(store, "a", name), true)
    await store.adopt(name, { settings, state: { cursor: 9 } })
    await assert.rejects(store.storage(name, "a").delete("state"), LeaseLost)
    assert.equal(await due(store, "b", name), true)
    assert.deepEqual(await store.read(name, "state"), { cursor: 9 })
    await store.close()
  })

  test(`${kind}: shutting down hands a listener straight over`, async () => {
    const store = await open()
    const name = unique()
    await store.activate(name, settings)
    assert.equal(await due(store, "a", name), true)
    await store.release("a", [name])
    assert.equal((await store.claim("b", [], 60000)).includes(name), true)
    await store.close()
  })
}
