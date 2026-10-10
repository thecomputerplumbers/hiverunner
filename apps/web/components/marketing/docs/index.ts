import { Agents } from "./agents"
import { Daemon } from "./daemon"
import { Faq } from "./faq"
import { LabelsAndSizes } from "./labels-and-sizes"
import { Pools } from "./pools"
import { Pricing } from "./pricing"
import { Quickstart } from "./quickstart"

/**
 * The docs, in sidebar order. Each page is a component in this folder, so
 * content is type-checked and can import prices instead of repeating them.
 */
export const DOCS = [
  {
    slug: "quickstart",
    title: "Quickstart",
    description: "Install the GitHub App, add a machine, route a workflow.",
    Body: Quickstart,
  },
  {
    slug: "agents",
    title: "Agents and MCP",
    description:
      "Connect Claude Code, Cursor or any MCP client and let it run your CI.",
    Body: Agents,
  },
  {
    slug: "labels-and-sizes",
    title: "Labels and sizes",
    description:
      "The hive, hive-4cpu and hive-8cpu labels and what a job runs on.",
    Body: LabelsAndSizes,
  },
  {
    slug: "daemon",
    title: "The daemon",
    description:
      "What runs on a developer's Mac, its states, and how it gives way.",
    Body: Daemon,
  },
  {
    slug: "hives",
    title: "Hives",
    description: "Who can add machines, and what happens when none is free.",
    Body: Pools,
  },
  {
    slug: "pricing",
    title: "Pricing",
    description: "The compute unit, the job price formula and receipts.",
    Body: Pricing,
  },
  {
    slug: "faq",
    title: "FAQ",
    description: "Short answers to common questions.",
    Body: Faq,
  },
] as const

export function findDoc(slug: string) {
  return DOCS.find((doc) => doc.slug === slug)
}
