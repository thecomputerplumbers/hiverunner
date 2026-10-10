import Link from "next/link"
import { RUNNER_SIZES } from "@workspace/pricing"
import { Code, CodeBlock } from "../code"
import { RouteDiagram } from "../route-diagram"
import { DAEMON_RELEASED, INSTALL_COMMAND } from "../site"
import { RouteLink } from "@/components/route-link"
import { ROUTED_WORKFLOW } from "@/lib/workflow"

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
        <li>A hive</li>
        <li>A balance, starting with a trial credit</li>
      </ul>

      <h2 id="add-a-machine">2. Add a machine</h2>
      <p>On an Apple Silicon Mac:</p>
      <CodeBlock title="Terminal" wrap>
        {INSTALL_COMMAND}
      </CodeBlock>
      <p>
        {DAEMON_RELEASED
          ? "It installs under your home directory and asks for no password. "
          : "Not released yet. "}
        See <Link href="/download">Download</Link> for{" "}
        {DAEMON_RELEASED ? "what it installs" : "status"}.
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
        Set <Code>label</Code> to a{" "}
        <Link href="/docs/labels-and-sizes">size</Link> and{" "}
        <Code>fallback</Code> to your current runner. Add{" "}
        <Code>needs: route</Code> and the <Code>runs-on</Code> line to each job
        you want routed. The step is{" "}
        <a href="https://github.com/thecomputerplumbers/hive-route">
          one short file you can read
        </a>
        : it asks whether a machine is free, waits up to fifteen seconds for
        one, and never fails your workflow.{" "}
        <Link href="/docs/routing">Routing and fallback</Link> has its inputs
        and what it checks.
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
