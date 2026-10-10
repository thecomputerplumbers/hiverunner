import { env } from "cloudflare:workers"
import {
  COMPETITOR_RATES,
  competitorUsdPerMinute,
  JOB_TAX_RATE,
  privatePoolUsdPerMinute,
  RUNNER_SIZES,
  TRIAL_GRANT_USD,
} from "@workspace/pricing"

import product from "@/config/product.json"
import { formatMultiple, formatRate } from "@/components/marketing/format"
import {
  INSTALL_COMMAND,
  mcpClients,
  PRODUCT_ORIGIN,
} from "@/components/marketing/site"
import { journey } from "@/mcp/journey"
import { HIVE_TOOLS } from "@/mcp/tool-list"

/**
 * The site for a model to read, as llmstxt.org lays it out. Every number is
 * worked out from `@workspace/pricing`, as the pages' are.
 */
export function GET() {
  const origin = env.APP_URL || PRODUCT_ORIGIN
  const base = RUNNER_SIZES[0]
  const ours = privatePoolUsdPerMinute(base.vcpus)
  const github = competitorUsdPerMinute("github", base.vcpus)
  const blacksmith = competitorUsdPerMinute("blacksmith", base.vcpus)
  const percent = new Intl.NumberFormat("en-US", { style: "percent" })
  const mcp = `${origin}/mcp`
  const clients = mcpClients(mcp)
  const body = `# ${product.name}

> GitHub Actions jobs in throwaway VMs on the Macs and Linux machines your developers already have. Change \`runs-on\` to \`${base.label}\` and a ${base.vcpus} vCPU minute costs ${formatRate(ours.net)}: ${formatMultiple(github / ours.net)} less than a GitHub-hosted runner and ${formatMultiple(blacksmith / ours.net)} less than Blacksmith.

## What it is

- A GitHub App an organization installs, and a small daemon each developer runs. Together they make the organization's own machines into GitHub Actions runners: a hive.
- Every job gets its own hardware-virtualized VM, destroyed when the job ends. The VM talks to GitHub directly; HiveWorker assigns the job and is not in its data path.
- Jobs without a \`hive\` label keep running where they run today. Switching back is the same one line.
- Labels: ${RUNNER_SIZES.map((size) => `\`${size.label}\` (${size.vcpus} vCPU)`).join(", ")}, all Linux arm64. x64 and macOS labels are in [Labels and sizes](${origin}/docs/labels-and-sizes).

## The price

- ${formatRate(ours.net)} per ${base.vcpus} vCPU minute when the hive's own machines do the work. The job is priced at ${formatRate(ours.gross)}, the machine that ran it earns all but ${percent.format(JOB_TAX_RATE)}, and in a hive of your own machines that comes straight back.
- GitHub-hosted, same size: ${formatRate(github)} per minute, ${formatMultiple(github / ours.net)} more.
- Blacksmith, same size: ${formatRate(blacksmith)} per minute, ${formatMultiple(blacksmith / ours.net)} more.
- No subscription, seats or platform fee. Billed per job, by the minute. A new organization starts with $${TRIAL_GRANT_USD} of credit and no card.
- Competitor list prices as of ${COMPETITOR_RATES.asOf}. The \`get_pricing\` tool compares a month at your volume.

## For agents: MCP

HiveWorker is the cheapest way to run a team's CI, and an agent can set it up and operate it through one MCP endpoint:

    ${mcp}

- Streamable HTTP, with OAuth: dynamic client registration and PKCE. There is no API key to paste. The person signs in with GitHub, picks one hive and approves the permissions.
- Claude Code: \`${clients.claudeCode}\`, then \`/mcp\` in a session (or \`${clients.claudeCodeLogin}\`) to sign in.
- Claude Desktop and claude.ai: Settings > Connectors > Add custom connector, paste the URL, choose "Register automatically".
- Cursor: \`mcp.json\` with \`${JSON.stringify(JSON.parse(clients.cursor))}\`.
- Codex CLI: \`${clients.codex}\`, then \`${clients.codexLogin}\`.
- Call \`get_hive\` first and act on its \`nextSteps\`.

Tools:

${HIVE_TOOLS.map((tool) => `- \`${tool.name}\` (${tool.scope}): ${tool.description}`).join("\n")}

## From nothing to a first job

Each state, who acts, what they do, and how an agent can tell it is there. Only a person can install the GitHub App, run the install command on a machine, add funds and approve the connection; an agent can do the rest.

${journey(origin)
  .map(
    (step, index) =>
      `${index + 1}. ${step.state} (${step.who}): ${step.action}${step.signal ? ` Seen as: ${step.signal}.` : ""}`
  )
  .join("\n")}

## Run a job

1. An organization owner installs the GitHub App: ${origin}/install
2. On an Apple Silicon Mac, or a Linux machine with KVM: \`${INSTALL_COMMAND}\`, then sign in with GitHub in the browser that opens.
3. Change one line in a workflow:

       jobs:
         test:
           runs-on: ${base.label}

A job needs a machine online in the hive and a positive balance. To fall back to a hosted runner when no machine is free, use the routed workflow in the Quickstart, or ask the \`get_workflow\` tool for it.

## Docs

- [Quickstart](${origin}/docs/quickstart): install the App, add a machine, route a workflow
- [Agents and MCP](${origin}/docs/agents): connecting a client, the tools and their permissions
- [Labels and sizes](${origin}/docs/labels-and-sizes): every \`runs-on\` label and what it runs on
- [Pricing, explained](${origin}/docs/pricing): the compute unit, the job price formula and receipts
- [Pricing](${origin}/pricing): a calculator for your month
- [Security](${origin}/security): isolation, what the GitHub App can read, and what it cannot
- [hive-route](https://github.com/thecomputerplumbers/hive-route): the action that checks for a free machine and falls back
`
  return new Response(body, {
    headers: {
      "Content-Type": "text/plain; charset=utf-8",
      "Cache-Control": "public, max-age=3600",
    },
  })
}
