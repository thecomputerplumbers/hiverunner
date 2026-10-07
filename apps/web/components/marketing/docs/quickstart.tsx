import Link from "next/link"
import { RUNNER_SIZES } from "@workspace/pricing"
import { Code, CodeBlock } from "../code"
import { RouteDiagram } from "../route-diagram"
import { COORDINATOR_ORIGIN, INSTALL_COMMAND } from "../site"
import { RouteLink } from "@/components/route-link"

/**
 * The recommended workflow: ask for a free machine first, and use the
 * GitHub-hosted runner when there is none.
 */
const ROUTED_WORKFLOW = `jobs:
  route:
    runs-on: ubuntu-latest
    permissions:
      id-token: write
    outputs:
      runner: \${{ steps.probe.outputs.runner }}
    env:
      HIVE: ${COORDINATOR_ORIGIN}
      LABEL: hive
      FALLBACK: ubuntu-latest
    steps:
      - id: probe
        run: |
          runner=$FALLBACK
          token=$(curl -fsS -H "Authorization: Bearer $ACTIONS_ID_TOKEN_REQUEST_TOKEN" \\
            "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=$HIVE" | jq -r .value) || token=
          deadline=$((SECONDS + 15))
          while [ -n "$token" ] && [ $SECONDS -lt $deadline ]; do
            if curl -fsS --max-time 5 -H "Authorization: Bearer $token" \\
              "$HIVE/v1/capacity?label=$LABEL" | jq -e .available > /dev/null; then
              runner=$LABEL
              break
            fi
            sleep 2
          done
          echo "runner=$runner" >> "$GITHUB_OUTPUT"

  test:
    needs: route
    runs-on: \${{ needs.route.outputs.runner }}
    steps:
      - uses: actions/checkout@v4
      - run: make test`

export function Quickstart() {
  return (
    <>
      <p>Install the App, add a Mac, route a workflow.</p>

      <h2 id="install">1. Install the GitHub App</h2>
      <p>
        Go to <RouteLink to="/install">Install</RouteLink> and pick your
        organization. If you are not an owner, GitHub asks one to approve.
      </p>
      <p>You get:</p>
      <ul>
        <li>
          Runner labels{" "}
          {RUNNER_SIZES.map((size, index) => (
            <span key={size.label}>
              {index > 0 ? ", " : null}
              <Code>{size.label}</Code>
            </span>
          ))}
        </li>
        <li>A private pool</li>
        <li>A wallet with a trial grant</li>
      </ul>

      <h2 id="add-a-machine">2. Add a machine</h2>
      <p>On an Apple Silicon Mac:</p>
      <CodeBlock title="Terminal" wrap>
        {INSTALL_COMMAND}
      </CodeBlock>
      <p>
        Not released yet. See <Link href="/download">Download</Link> for status.
      </p>
      <p>
        A browser window opens. Sign in with GitHub and approve the machine. It
        runs a short benchmark and shows as online.
      </p>

      <h2 id="route-a-workflow">3. Route a workflow</h2>
      <p>
        Macs come and go. Check for a free machine before each run, and fall
        back when there is none:
      </p>
      <RouteDiagram />
      <CodeBlock title=".github/workflows/ci.yml">{ROUTED_WORKFLOW}</CodeBlock>
      <p>
        Set <Code>LABEL</Code> to a{" "}
        <Link href="/docs/labels-and-sizes">size</Link> and{" "}
        <Code>FALLBACK</Code> to your current runner. Add{" "}
        <Code>needs: route</Code> and the <Code>runs-on</Code> line to each job
        you want routed.
      </p>

      <h2 id="good-to-know">Good to know</h2>
      <ul>
        <li>
          <Code>hive</Code> is arm64 and <Code>ubuntu-latest</Code> is x64. If a
          job needs one architecture, fall back to <Code>ubuntu-24.04-arm</Code>
          .
        </li>
        <li>
          The check does not reserve a machine. If it leaves before the job
          starts, the job waits in GitHub's queue.
        </li>
        <li>
          For jobs that can wait, skip <Code>route</Code> and set{" "}
          <Code>runs-on: hive</Code>.
        </li>
      </ul>
    </>
  )
}
