/**
 * The scale set listeners run outside Cloudflare (apps/listener), reached
 * through a tunnel. A long poll in a Durable Object is billed for all the
 * time it waits.
 */
function send(env: CoordinatorEnv, path: string, init?: RequestInit) {
  if (!env.LISTENER_URL || !env.LISTENER_API_TOKEN)
    throw new Error("Listener service is not configured")
  return fetch(new URL(path, env.LISTENER_URL), {
    ...init,
    headers: {
      Authorization: `Bearer ${env.LISTENER_API_TOKEN}`,
      "Content-Type": "application/json",
    },
  })
}

async function checked(response: Promise<Response>) {
  const result = await response
  if (!result.ok) throw new Error(`Listener responded ${result.status}`)
  return result
}

export const listenerConfigured = (env: CoordinatorEnv) =>
  Boolean(env.LISTENER_URL && env.LISTENER_API_TOKEN)

export async function activateListeners(
  env: CoordinatorEnv,
  organizationId: string,
  labels: string[]
) {
  await checked(
    send(env, "/v1/activate", {
      method: "POST",
      body: JSON.stringify({ organizationId, labels }),
    })
  )
}

export async function listenerWaiting(env: CoordinatorEnv, name: string) {
  const response = await checked(
    send(env, `/v1/waiting?name=${encodeURIComponent(name)}`)
  )
  return ((await response.json()) as { waiting: number }).waiting
}
