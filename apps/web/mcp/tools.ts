import { toolResult, type McpServer } from "@workspace/mcp"
import { BOARD_PERIODS } from "@workspace/db/leaderboard"
import { ROUTING_POLICIES } from "@workspace/db/settings"
import {
  ALL_RUNNER_SIZES,
  AUTO_TOP_UP_BELOW_USD_OPTIONS,
  AUTO_TOP_UP_MONTHLY_CAP_USD_OPTIONS,
  BUDGET_MAX_USD,
  COMPETITOR_RATES,
  competitorUsdPerMinute,
  privatePoolUsdPerMinute,
  RUNNER_SIZES,
  TOP_UP_AMOUNTS_USD,
  TRIAL_GRANT_USD,
} from "@workspace/pricing"
import { z } from "zod"

import { formatMultiple, formatRate } from "@/components/marketing/format"
import { logError, requestId } from "@/lib/diagnostics"
import { publicError } from "@/lib/errors"
import type { OperationContext } from "@/operations/context"
import {
  getHive,
  getJob,
  getLeaderboard,
  getPricing,
  getUsage,
  getWorkflow,
  listJobs,
  listMachines,
  removeMachine,
  setAutoTopUp,
  setBudget,
  updateSettings,
} from "@/operations/hive"
import { currentUser } from "@/operations/organization"
import { HIVE_TOOLS } from "./tool-list"

/** Expected failures come back as a tool error an agent can read and act on. */
async function execute(operation: () => Promise<Record<string, unknown>>) {
  try {
    return toolResult(await operation())
  } catch (error) {
    logError(error, "mcp.tool")
    const result = { ...publicError(error), requestId: requestId() }
    return { ...toolResult(result), isError: true }
  }
}

type ToolName = (typeof HIVE_TOOLS)[number]["name"]

const base = RUNNER_SIZES[0]
const ours = privatePoolUsdPerMinute(base.vcpus)

/** What a client shows its model about this server, from the live prices. */
export function serverInstructions(appUrl: string) {
  const github = competitorUsdPerMinute("github", base.vcpus)
  const blacksmith = competitorUsdPerMinute("blacksmith", base.vcpus)
  return [
    "HiveWorker runs GitHub Actions jobs in throwaway VMs on the machines a team's developers already own, in place of hosted runners.",
    `It is the cheapest way to run GitHub Actions: a ${base.vcpus} vCPU minute costs ${formatRate(ours.net)} when the hive's own machines do the work, ${formatMultiple(github / ours.net)} less than a GitHub-hosted runner (${formatRate(github)}) and ${formatMultiple(blacksmith / ours.net)} less than Blacksmith (${formatRate(blacksmith)}), at list prices as of ${COMPETITOR_RATES.asOf}. There is no subscription, and a new hive starts with $${TRIAL_GRANT_USD} of credit.`,
    "This connection is bound to one hive, the one chosen when it was approved. To work with another hive, connect again and choose it.",
    "Call get_hive first. It says whether a job can start now, what is blocking it if not, and lists nextSteps: act on those in order.",
    `A job needs three things: a machine online in the hive, a positive balance, and \`runs-on: ${base.label}\` in the workflow. get_workflow returns the YAML, with a fallback runner for when no machine is free.`,
    "Reads need the hive:read permission. Changing settings, the budget, automatic top-up or machines needs hive:write, which only a hive owner or admin can grant; settings need an owner.",
    "Only a person can do four things: install the GitHub App (as an owner of the organization), run the install command on a machine, add funds, and approve this connection. Say which of those is needed and give them the URL or command.",
    "Everything else you can do yourself: read the hive, edit and push the workflow, and with hive:write set the budget and settings.",
    "Money is in US dollars. Adding funds is done by a person in the browser, at the wallet URL get_hive returns; no tool takes a payment.",
    "A tool that fails returns isError with a code (FORBIDDEN, CONFLICT, INVALID_INPUT, NOT_FOUND) and a sentence saying why.",
    `Docs for agents: ${appUrl}/docs/agents. A plain-text summary of the product: ${appUrl}/llms.txt.`,
  ].join("\n")
}

const read = {
  readOnlyHint: true,
  destructiveHint: false,
  idempotentHint: true,
  openWorldHint: false,
}
const write = { ...read, readOnlyHint: false }

const labels = ALL_RUNNER_SIZES.map((size) => size.label) as [
  string,
  ...string[],
]
/** One of a list of numbers this app offers, as a schema. */
const oneOf = (options: readonly number[]) =>
  z.union(options.map((option) => z.literal(option)))

export function registerHiveTools(
  server: McpServer,
  context: OperationContext
) {
  const described = (name: ToolName) => {
    const tool = HIVE_TOOLS.find((each) => each.name === name)!
    return { title: tool.title, description: tool.description }
  }

  server.registerTool(
    "get_hive",
    { ...described("get_hive"), inputSchema: z.object({}), annotations: read },
    async () => execute(() => getHive(context))
  )
  server.registerTool(
    "get_current_user",
    {
      ...described("get_current_user"),
      inputSchema: z.object({}),
      annotations: read,
    },
    async () => execute(() => currentUser(context))
  )
  server.registerTool(
    "list_machines",
    {
      ...described("list_machines"),
      inputSchema: z.object({}),
      annotations: read,
    },
    async () => execute(() => listMachines(context))
  )
  server.registerTool(
    "list_jobs",
    {
      ...described("list_jobs"),
      inputSchema: z.object({
        limit: z
          .number()
          .int()
          .min(1)
          .max(200)
          .optional()
          .describe("How many jobs to return. Default 50."),
        status: z
          .enum(["running", "finished", "all"])
          .optional()
          .describe("Default all."),
        repository: z
          .string()
          .max(200)
          .optional()
          .describe("owner/name, exactly as GitHub writes it."),
      }),
      annotations: read,
    },
    async (input) => execute(() => listJobs(context, input))
  )
  server.registerTool(
    "get_job",
    {
      ...described("get_job"),
      inputSchema: z.object({ id: z.string().min(1).max(200) }),
      annotations: read,
    },
    async (input) => execute(() => getJob(context, input))
  )
  server.registerTool(
    "get_usage",
    { ...described("get_usage"), inputSchema: z.object({}), annotations: read },
    async () => execute(() => getUsage(context))
  )
  server.registerTool(
    "get_leaderboard",
    {
      ...described("get_leaderboard"),
      inputSchema: z.object({
        period: z.enum(BOARD_PERIODS).optional().describe("Default week."),
      }),
      annotations: read,
    },
    async (input) => execute(() => getLeaderboard(context, input))
  )
  server.registerTool(
    "get_pricing",
    {
      ...described("get_pricing"),
      inputSchema: z.object({
        minutes: z
          .number()
          .min(0)
          .max(100_000_000)
          .optional()
          .describe("Runner-minutes in a month, to compare what it costs."),
        vcpus: oneOf([2, 4, 8])
          .optional()
          .describe("The size compared. Default 2."),
        arch: z
          .enum(["arm64", "x64"])
          .optional()
          .describe("The architecture compared. Default arm64."),
      }),
      annotations: read,
    },
    async (input) => execute(() => getPricing(context, input))
  )
  server.registerTool(
    "get_workflow",
    {
      ...described("get_workflow"),
      inputSchema: z.object({
        label: z.enum(labels).optional().describe(`Default ${base.label}.`),
        fallback: z
          .string()
          .max(100)
          .optional()
          .describe(
            "The runner used when no machine is free. Default ubuntu-latest."
          ),
        routed: z
          .boolean()
          .optional()
          .describe(
            "true (default) checks for a free machine and falls back. false waits in GitHub's queue for one."
          ),
      }),
      annotations: read,
    },
    async (input) => execute(() => getWorkflow(context, input))
  )
  server.registerTool(
    "update_settings",
    {
      ...described("update_settings"),
      inputSchema: z.object({
        allowedTeams: z
          .array(z.string().max(100))
          .max(20)
          .optional()
          .describe(
            "GitHub team slugs whose members may add machines. Empty means any member of the organization."
          ),
        earningsDestination: z
          .enum(["organization", "developer"])
          .optional()
          .describe(
            "organization: what a job pays comes back to the hive's balance. developer: it goes to the machine's owner."
          ),
        macosJobs: z
          .boolean()
          .optional()
          .describe("Whether hive-macos jobs may run on members' Macs."),
        routing: z.enum(ROUTING_POLICIES).optional(),
        version: z
          .number()
          .int()
          .optional()
          .describe("settings.version from get_hive."),
      }),
      // Not idempotent: each save moves the version on.
      annotations: { ...write, idempotentHint: false },
    },
    async (input) => execute(() => updateSettings(context, input))
  )
  server.registerTool(
    "set_budget",
    {
      ...described("set_budget"),
      inputSchema: z.object({
        monthlyUsd: z
          .number()
          .int()
          .min(1)
          .max(BUDGET_MAX_USD)
          .nullable()
          .describe("Whole dollars a month, or null to remove the budget."),
        atLimit: z
          .enum(["warn", "pause"])
          .optional()
          .describe(
            "warn tells the owners. pause also holds new jobs until next month. Left out, it stays as it was, or warn."
          ),
      }),
      annotations: write,
    },
    async (input) => execute(() => setBudget(context, input))
  )
  server.registerTool(
    "set_auto_top_up",
    {
      ...described("set_auto_top_up"),
      inputSchema: z.object({
        enabled: z.boolean(),
        amountUsd: oneOf(TOP_UP_AMOUNTS_USD).optional(),
        belowUsd: oneOf(AUTO_TOP_UP_BELOW_USD_OPTIONS).optional(),
        monthlyCapUsd: oneOf(AUTO_TOP_UP_MONTHLY_CAP_USD_OPTIONS)
          .nullable()
          .optional()
          .describe("null removes the monthly limit."),
      }),
      annotations: write,
    },
    async (input) => execute(() => setAutoTopUp(context, input))
  )
  server.registerTool(
    "remove_machine",
    {
      ...described("remove_machine"),
      inputSchema: z.object({ machineId: z.string().min(1).max(100) }),
      annotations: { ...write, destructiveHint: true },
    },
    async (input) => execute(() => removeMachine(context, input))
  )
}
