import { RUNNER_SIZES } from "@workspace/pricing"

import { INSTALL_COMMAND, mcpClients } from "@/components/marketing/site"

/**
 * The way from nothing to a first job: each state someone can be in, who has
 * to act, what they do, and how an agent can tell it is there. The docs page,
 * `llms.txt` and the endpoint's 401 are all written from this, so an agent is
 * told the same thing wherever it looks.
 */
export function journey(origin: string) {
  const label = RUNNER_SIZES[0].label
  return [
    {
      state: "Not connected",
      who: "agent",
      action: `Add the MCP server: \`${mcpClients(`${origin}/mcp`).claudeCode}\`, or the same URL in any MCP client. The client opens a browser.`,
      signal: "401 from /mcp",
    },
    {
      state: "Not signed in",
      who: "person",
      action: "Sign in with GitHub. There is nothing else to set up.",
      signal: "the browser",
    },
    {
      state: "Not in a hive",
      who: "person",
      action: `Install HiveWorker on a GitHub organization you own, at ${origin}/install. Someone who is not an owner requests it and waits for an owner; a personal account cannot be a hive. The consent page continues when you come back.`,
      signal: "the consent page says so; the connection fails until then",
    },
    {
      state: "Choosing a hive",
      who: "person",
      action: "Pick the hive and allow the permissions.",
      signal: "tokens arrive",
    },
    {
      state: "No machine",
      who: "person",
      action: `On an Apple Silicon Mac or a Linux machine with KVM, run \`${INSTALL_COMMAND}\`, then sign in with GitHub in the browser that opens. Any member of the organization can.`,
      signal: "get_hive.nextSteps, machines.total === 0",
    },
    {
      state: "Machine but none connected",
      who: "person",
      action: "Run `hived run`, or open the machine's lid.",
      signal: 'jobs.blockedBy === "no-machine-online"',
    },
    {
      state: "No job yet",
      who: "agent",
      action: `Edit the workflow to \`runs-on: ${label}\`: call get_workflow, commit, push.`,
      signal: "lifetime.jobs === 0",
    },
    {
      state: "Balance out or budget spent",
      who: "person",
      action:
        "Add funds at the wallet URL get_hive returns, or raise the budget (set_budget).",
      signal: "jobs.blockedBy",
    },
    {
      state: "Running",
      who: "agent",
      action: "Watch it with list_jobs, get_job and get_usage.",
      signal: null,
    },
  ] as const
}

/** The same path as a few short lines, for an agent to relay. */
export function journeySteps(origin: string) {
  return [
    `Connect: add ${origin}/mcp to your MCP client; it opens a browser.`,
    "Person: sign in with GitHub.",
    `Person, if in no hive: install HiveWorker on a GitHub organization they own (${origin}/install). The consent page says how, and continues once they are back.`,
    "Person: pick the hive and allow the permissions.",
    "Agent: call get_hive and act on nextSteps.",
    `Person: add a machine with \`${INSTALL_COMMAND}\`, and keep it running.`,
    `Agent: set runs-on: ${RUNNER_SIZES[0].label} in a workflow (get_workflow), commit and push.`,
  ]
}
