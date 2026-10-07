import Link from "next/link"
import { RUNNER_SIZES } from "@workspace/pricing"
import { Code, CodeBlock } from "../code"
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
      <p>
        Three things have to happen once: an owner installs the GitHub App, at
        least one developer adds a Mac, and a workflow asks for a{" "}
        <Code>hive</Code> runner.
      </p>

      <h2 id="install">1. Install the GitHub App</h2>
      <p>
        Go to <RouteLink to="/install">Install</RouteLink> and choose your
        organization. You need to be an organization owner. If you are not,
        GitHub sends the request to the owners and the install completes when
        one of them approves it.
      </p>
      <p>Installing creates three things:</p>
      <ul>
        <li>
          The runner labels{" "}
          {RUNNER_SIZES.map((size, index) => (
            <span key={size.label}>
              {index > 0 ? ", " : null}
              <Code>{size.label}</Code>
            </span>
          ))}
          .
        </li>
        <li>A private pool for your organization's machines.</li>
        <li>A wallet for the organization, holding the trial grant.</li>
      </ul>
      <p>
        HiveWorker works with GitHub organizations. Personal accounts are not
        supported in the first release.
      </p>

      <h2 id="add-a-machine">2. Add a machine</h2>
      <p>On an Apple Silicon Mac, install the daemon:</p>
      <CodeBlock title="Terminal" wrap>
        {INSTALL_COMMAND}
      </CodeBlock>
      <p>
        The daemon is not released yet, so this command does not work today. See{" "}
        <Link href="/download">Download</Link> for status.
      </p>
      <p>
        The daemon opens a browser window. Sign in with GitHub and approve the
        machine. If you belong to more than one organization that uses
        HiveWorker, you pick which pool the machine joins. The daemon then runs
        a short benchmark and reports as online.
      </p>

      <h2 id="route-a-workflow">3. Route a workflow to your pool</h2>
      <p>
        Your pool is made of your colleagues' Macs, so a machine is not always
        free: lids close and people go home. Do not point a job straight at{" "}
        <Code>hive</Code>. Add a short <Code>route</Code> job that checks for a
        free machine first, and let the real job run wherever it says:
      </p>
      <CodeBlock title=".github/workflows/ci.yml">{ROUTED_WORKFLOW}</CodeBlock>
      <p>What this does on every run:</p>
      <ul>
        <li>
          <Code>route</Code> asks HiveWorker whether a machine is free for{" "}
          <Code>LABEL</Code>. It stops the moment one is, and gives up after 15
          seconds.
        </li>
        <li>
          If a machine is free, <Code>test</Code> runs on your pool. If not, or
          if HiveWorker cannot be reached, it runs on <Code>FALLBACK</Code>, the
          runner the job used before.
        </li>
        <li>
          Either way the job reports to GitHub as usual: logs, artifacts and
          checks appear where they always have.
        </li>
      </ul>
      <p>
        To adapt it, set <Code>LABEL</Code> to the{" "}
        <Link href="/docs/labels-and-sizes">size</Link> you want and{" "}
        <Code>FALLBACK</Code> to your current runner, then add{" "}
        <Code>needs: route</Code> and the <Code>runs-on</Code> line to each job
        you want routed. The <Code>id-token: write</Code> permission is how the
        check proves which organization is asking. It needs no secret.
      </p>

      <h2 id="without-a-fallback">Without a fallback</h2>
      <p>
        You can set <Code>runs-on: hive</Code> directly and skip the{" "}
        <Code>route</Code> job. Then a job waits in GitHub's queue until a
        machine is free, however long that takes. Only do this for jobs that can
        wait.
      </p>

      <h2 id="next">What to check next</h2>
      <ul>
        <li>
          HiveWorker jobs run in a Linux arm64 VM, and{" "}
          <Code>ubuntu-latest</Code> is x64. A routed job has to work on both,
          or fall back to an arm64 runner such as <Code>ubuntu-24.04-arm</Code>.
          See <Link href="/docs/labels-and-sizes">Labels and sizes</Link>.
        </li>
        <li>
          The check does not reserve a machine. If the machine leaves between
          the check and the job starting, the job waits in GitHub's queue. See{" "}
          <Link href="/docs/pools">Pools</Link>.
        </li>
      </ul>
    </>
  )
}
