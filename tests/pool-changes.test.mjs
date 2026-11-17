import assert from "node:assert/strict"
import { test } from "node:test"
import { database } from "./database.mjs"
import { dispatchPoolChanges } from "../apps/coordinator/src/pool-changes.ts"

function fixture(t) {
  const data = database(t)
  const { sqlite } = data
  sqlite.exec(`
    PRAGMA foreign_keys = ON;
    INSERT INTO user (id,name,email,email_verified,created_at,updated_at) VALUES ('u','U','u@example.test',1,0,0);
    INSERT INTO organization (id,name,slug,created_at) VALUES ('a','A','a',0),('b','B','b',0);
    INSERT INTO github_installation (account_id,installation_id,organization_id,account_login,account_type,repository_selection,status,created_at,updated_at)
      VALUES (1,1,'a','a','Organization','all','active',0,0),(2,2,'b','b','Organization','all','active',0,0);
    INSERT INTO machine (id,user_id,name,model,os,arch,cores,memory_bytes,speed_score,daemon_version,token_hash,created_at)
      VALUES ('m','u','M','test','linux','x64',4,1,1,'1','hash',0);
    INSERT INTO machine_pool (machine_id,organization_id,created_at) VALUES ('m','a',0),('m','b',0);
    DELETE FROM pool_change;
  `)
  const pending = () =>
    sqlite
      .prepare("SELECT organization_id FROM pool_change ORDER BY 1")
      .all()
      .map((x) => x.organization_id)
  const clear = () => sqlite.exec("DELETE FROM pool_change")
  return { ...data, pending, clear }
}

test("access changes coalesce atomically; ordinary traffic does not queue a sweep", (t) => {
  const { sqlite, pending, clear } = fixture(t)
  sqlite.exec(
    "UPDATE machine_pool SET presence='online', last_seen_at=123; UPDATE machine SET daemon_version='2'; UPDATE machine_pool SET status='active'"
  )
  assert.deepEqual(pending(), [])
  sqlite.exec(
    "UPDATE machine_pool SET status='revoked' WHERE organization_id='a'"
  )
  assert.deepEqual(pending(), ["a"])
  sqlite.exec("UPDATE machine SET status='revoked' WHERE id='m'")
  assert.deepEqual(pending(), ["a", "b"])
  clear()
  sqlite.exec(
    "UPDATE github_installation SET status='suspended' WHERE organization_id='a'"
  )
  assert.deepEqual(pending(), ["a"])
  clear()
  sqlite.exec(
    "BEGIN; UPDATE github_installation SET status='removed' WHERE organization_id='b'; ROLLBACK;"
  )
  assert.deepEqual(pending(), [])
  sqlite.exec("DELETE FROM machine_pool WHERE organization_id='b'")
  assert.deepEqual(pending(), ["b"])
  clear()
  sqlite.exec(
    "INSERT INTO machine_pool (machine_id,organization_id,created_at) VALUES ('m','b',0)"
  )
  assert.deepEqual(pending(), ["b"])
})

test("deletes notify affected pools even when the organization or machine disappears", (t) => {
  const { sqlite, pending, clear } = fixture(t)
  sqlite.exec("DELETE FROM organization WHERE id='a'")
  assert.deepEqual(pending(), ["a"])
  clear()
  sqlite.exec("DELETE FROM machine WHERE id='m'")
  assert.deepEqual(pending(), ["b"])
})

test("enabling macOS wakes existing capacity without recurring activation", (t) => {
  const { sqlite, pending, clear } = fixture(t)
  sqlite.exec(
    "INSERT INTO pool_settings (organization_id,updated_at) VALUES ('a',0)"
  )
  clear()
  sqlite.exec("UPDATE pool_settings SET macos_jobs=1 WHERE organization_id='a'")
  assert.deepEqual(pending(), ["a"])
  clear()
  sqlite.exec("UPDATE pool_settings SET macos_jobs=1 WHERE organization_id='a'")
  assert.deepEqual(pending(), [])
})

test("delivery acknowledges only the revision it read, then leaves unchanged pools asleep", async (t) => {
  const { sqlite, binding, pending } = fixture(t)
  sqlite.exec(
    "UPDATE machine_pool SET status='revoked' WHERE organization_id='a'"
  )
  let calls = 0
  const env = {
    DB: binding,
    POOL: {
      getByName: (name) => ({
        reconcile: async () => {
          assert.equal(name, "a")
          if (++calls === 1)
            sqlite.exec(
              "UPDATE machine_pool SET status='active' WHERE organization_id='a'"
            )
        },
      }),
    },
  }
  await dispatchPoolChanges(env)
  assert.deepEqual(
    pending(),
    ["a"],
    "a change arriving during delivery must survive"
  )
  await dispatchPoolChanges(env)
  assert.deepEqual(pending(), [])
  await dispatchPoolChanges(env)
  assert.equal(calls, 2, "an empty dispatch must not wake any object")
})

test("failed delivery remains durable without blocking other pools", async (t) => {
  const { sqlite, binding, pending } = fixture(t)
  sqlite.exec("UPDATE machine SET status='revoked'")
  let fail = true
  const seen = []
  const env = {
    DB: binding,
    POOL: {
      getByName: (name) => ({
        reconcile: async () => {
          seen.push(name)
          if (name === "a" && fail) throw new Error("unavailable")
        },
      }),
    },
  }
  await assert.rejects(dispatchPoolChanges(env), /reconciliation failed/)
  assert.deepEqual(pending(), ["a"])
  fail = false
  await dispatchPoolChanges(env)
  assert.deepEqual(pending(), [])
  assert.deepEqual(seen.sort(), ["a", "a", "b"])
})
