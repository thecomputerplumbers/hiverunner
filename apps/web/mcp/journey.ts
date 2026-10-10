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

/**
 * One prompt a person pastes into their coding agent to be taken from
 * nothing to a first job. It names the reference to read, the order to work
 * in and what only the person can do, and leaves the detail to `llms.txt`
 * and `get_hive`, so it does not go stale when a step changes.
 */
export function setupPrompt(origin: string) {
  const clients = mcpClients(`${origin}/mcp`)
  const label = RUNNER_SIZES[0].label
  return `Set up HiveWorker for this repository and guide me through it.

HiveWorker runs GitHub Actions jobs on Macs and Linux machines my team already owns. The workflows stay on GitHub Actions; only \`runs-on\` changes, and the minutes cost far less than on a GitHub-hosted runner.

Start by reading ${origin}/llms.txt. It is the current reference for everything below.

Work through these steps in order. Do what you can yourself. When a step needs me, tell me exactly what to do, then wait and check that it worked before moving on.

1. Connect to HiveWorker's MCP server at ${origin}/mcp.
   - Claude Code: \`${clients.claudeCode}\`, then I sign in with \`/mcp\`.
   - Codex: \`${clients.codex}\`, then \`${clients.codexLogin}\`.
   - Cursor or another client: add the URL as an MCP server.
   A browser opens. I sign in with GitHub, pick a hive and approve the permissions. If I am not in a hive yet, the page tells me to install HiveWorker on a GitHub organization I own (${origin}/install).
2. Call \`get_hive\` and follow its \`nextSteps\` until nothing blocks a job.
3. If the hive has no machine connected, have me run this on an Apple Silicon Mac or a Linux machine with KVM and sign in when the browser opens:
   \`${INSTALL_COMMAND}\`
   Confirm with \`list_machines\` that it is connected.
4. Find the GitHub Actions workflows in this repository. Call \`get_workflow\` and propose changing one job to \`runs-on: ${label}\`, with the fallback to a GitHub-hosted runner for when no machine is free. If there are several jobs, ask me which to start with. Show me the diff before you commit, and do not push without asking.
5. After the push, watch the run with \`list_jobs\` and \`get_job\`. Then tell me what it cost and what the same minutes cost on a GitHub-hosted runner (\`get_usage\`).

Rules:
- Only I can sign in, install the GitHub App, run the install command and add funds. Never ask me for a password, token or API key; HiveWorker has none to give you.
- If you cannot use MCP here, follow ${origin}/docs/quickstart instead and tell me each step.
- Keep me informed in a sentence or two per step, and stop if something looks wrong.`
}
