import { DatabaseSync } from "node:sqlite"
import { readdirSync, readFileSync } from "node:fs"
import { createAuthDb } from "../packages/auth/src/db.ts"

// Execute the real D1 queries and generated migrations against SQLite. Only
// the transport is adapted; constraints, upserts and RETURNING are real SQL.
export function database(t) {
  const sqlite = new DatabaseSync(":memory:")
  t.after(() => sqlite.close())
  const directory = new URL("../apps/web/migrations/", import.meta.url)
  for (const folder of readdirSync(directory).sort()) {
    sqlite.exec(
      readFileSync(new URL(`${folder}/migration.sql`, directory), "utf8")
    )
  }
  function prepare(query, args = []) {
    return {
      bind: (...values) => prepare(query, values),
      raw: async () => {
        const stmt = sqlite.prepare(query)
        stmt.setReturnArrays(true)
        return stmt.all(...args)
      },
      all: async () => ({ results: sqlite.prepare(query).all(...args) }),
      run: async () => sqlite.prepare(query).run(...args),
    }
  }
  return { db: createAuthDb({ prepare }), sqlite, binding: { prepare } }
}
