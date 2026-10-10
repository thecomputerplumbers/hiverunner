/**
 * What every tool is called and says it does. The docs page, `llms.txt` and
 * the server all read this list, so none of them can name a tool that is not
 * there. Kept apart from the registrations so a page can import it without
 * the operations behind them.
 */
export const HIVE_TOOLS = [
  {
    name: "get_hive",
    title: "Hive overview",
    scope: "hive:read",
    role: "Member",
    description:
      "Everything about the connected hive in one call: balance, whether a job can start now and what blocks it, machines, labels and prices, budget, settings, lifetime savings, and nextSteps to act on. Call this first.",
  },
  {
    name: "get_current_user",
    title: "Signed-in account",
    scope: "hive:read",
    role: "Member",
    description:
      "The person this connection acts as. Use it to tell which machines and jobs are theirs.",
  },
  {
    name: "list_machines",
    title: "Machines",
    scope: "hive:read",
    role: "Member",
    description:
      "Every machine serving the hive: owner, cores, architecture, presence, daemon version and recent work. Use it when get_hive says no machine is online, or before remove_machine.",
  },
  {
    name: "list_jobs",
    title: "Jobs",
    scope: "hive:read",
    role: "Member",
    description:
      "Recent jobs, newest first, with result, duration and price. Filter by status or repository; call get_job for one job's receipt.",
  },
  {
    name: "get_job",
    title: "Job receipt",
    scope: "hive:read",
    role: "Member",
    description:
      "One job by id, with its price split and the signed receipt once it has finished. Ids come from list_jobs.",
  },
  {
    name: "get_usage",
    title: "Usage",
    scope: "hive:read",
    role: "Member",
    description:
      "The last fourteen days by day: jobs, failures, minutes, what was spent and what the same minutes list for on GitHub-hosted runners, plus the daily balance.",
  },
  {
    name: "get_leaderboard",
    title: "Leaderboard",
    scope: "hive:read",
    role: "Member",
    description:
      "Who and which machines carried the hive's jobs over the last week, month or all time.",
  },
  {
    name: "get_pricing",
    title: "Pricing",
    scope: "hive:read",
    role: "Member",
    description:
      "The per-minute price of every label beside GitHub-hosted and Blacksmith list prices. Pass minutes to compare a month. Use it to answer what moving a workflow would save.",
  },
  {
    name: "get_workflow",
    title: "Workflow YAML",
    scope: "hive:read",
    role: "Member",
    description:
      "The workflow YAML that runs a job on the hive, with a fallback runner for when no machine is free. Use it when editing a repository's .github/workflows files.",
  },
  {
    name: "update_settings",
    title: "Change hive settings",
    scope: "hive:write",
    role: "Owner",
    description:
      "Change who may add machines (allowedTeams), who is paid (earningsDestination), whether macOS jobs run, or how jobs are routed. Only what you pass changes. Pass the version from get_hive to refuse saving over someone else's change.",
  },
  {
    name: "set_budget",
    title: "Set the monthly budget",
    scope: "hive:write",
    role: "Admin or owner",
    description:
      "Set, change or remove the hive's monthly budget for jobs, and whether reaching it warns or pauses new jobs. Pass monthlyUsd: null to remove it.",
  },
  {
    name: "set_auto_top_up",
    title: "Automatic top-up",
    scope: "hive:write",
    role: "Admin or owner",
    description:
      "Turn automatic top-up on or off, or change the amount, the balance it acts below and its monthly limit. It charges the card saved by a payment made in the browser; a card cannot be added here.",
  },
  {
    name: "remove_machine",
    title: "Remove a machine",
    scope: "hive:write",
    role: "Machine owner or hive owner",
    description:
      "Stop a machine serving this hive. It keeps serving its other hives, and its owner can add it back with `hived link`. Ids come from list_machines.",
  },
] as const
