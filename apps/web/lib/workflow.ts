/**
 * The recommended workflow: ask for a free machine first, and use the
 * GitHub-hosted runner when there is none. The quickstart shows it and the
 * `get_workflow` tool hands it to agents, so both are written from here.
 */
export function routedWorkflow({
  label = "hive",
  fallback = "ubuntu-latest",
}: { label?: string; fallback?: string } = {}) {
  return `jobs:
  route:
    runs-on: ubuntu-latest
    permissions:
      id-token: write
    outputs:
      runner: \${{ steps.hive.outputs.runner }}
    steps:
      - id: hive
        uses: thecomputerplumbers/hive-route@v1
        with:
          label: ${label}
          fallback: ${fallback}

  test:
    needs: route
    runs-on: \${{ needs.route.outputs.runner }}
    steps:
      - uses: actions/checkout@v7
      - uses: oven-sh/setup-bun@v2
      - run: bun test`
}

export const ROUTED_WORKFLOW = routedWorkflow()

/** For jobs that can wait for a machine: no probe, no fallback. */
export function plainWorkflow(label = "hive") {
  return `jobs:
  test:
    runs-on: ${label}
    steps:
      - uses: actions/checkout@v7
      - uses: oven-sh/setup-bun@v2
      - run: bun test`
}
