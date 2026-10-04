import assert from "node:assert/strict"
import { test } from "node:test"
import { githubTestAPI } from "../apps/coordinator/src/config.ts"

test("coordinator GitHub overrides require explicit local loopback configuration", () => {
  assert.equal(githubTestAPI({}), undefined)
  assert.equal(githubTestAPI({ LOCAL_DEVELOPMENT: "1" }), undefined)
  for (const url of ["http://localhost:1234", "http://127.0.0.1:1234"]) {
    assert.throws(
      () => githubTestAPI({ GITHUB_API_URL: url }),
      /local loopback/
    )
    assert.equal(
      githubTestAPI({ GITHUB_API_URL: url, LOCAL_DEVELOPMENT: "1" }),
      url
    )
  }
  for (const url of [
    "https://evil.example",
    "http://localhost.evil.example",
    "http://user:password@localhost",
    "http://localhost/path",
    "http://localhost?query=secret",
    "http://localhost#fragment",
  ]) {
    assert.throws(
      () => githubTestAPI({ GITHUB_API_URL: url, LOCAL_DEVELOPMENT: "1" }),
      /local loopback/
    )
  }
})
