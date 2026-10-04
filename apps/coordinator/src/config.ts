/** A fake API is allowed only in explicitly local, loopback-only fixtures. */
export function githubTestAPI(env: {
  GITHUB_API_URL?: string
  LOCAL_DEVELOPMENT?: string
}) {
  if (!env.GITHUB_API_URL) return undefined
  const url = new URL(env.GITHUB_API_URL)
  if (
    env.LOCAL_DEVELOPMENT !== "1" ||
    url.protocol !== "http:" ||
    !["localhost", "127.0.0.1"].includes(url.hostname) ||
    url.username ||
    url.password ||
    url.pathname !== "/" ||
    url.search ||
    url.hash
  )
    throw new Error("GitHub API override requires local loopback development")
  return url.origin
}
