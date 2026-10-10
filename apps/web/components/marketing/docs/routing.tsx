import Link from "next/link"
import { RUNNER_SIZES } from "@workspace/pricing"
import { Code, CodeBlock } from "../code"
import { RouteDiagram } from "../route-diagram"
import { ROUTED_WORKFLOW, plainWorkflow } from "@/lib/workflow"

const HIVE_ROUTE = "https://github.com/thecomputerplumbers/hive-route"

export function Routing() {
  return (
    <>
      <p>
        Route every workflow you move to HiveWorker. A routed job runs on your
        hive when one of its machines is free and on the runner you use now when
        none is, so a closed laptop never leaves a job waiting. It is one small
        job in front of the ones you already have.
      </p>
      <RouteDiagram />

      <h2 id="the-workflow">The workflow</h2>
      <CodeBlock title=".github/workflows/ci.yml">{ROUTED_WORKFLOW}</CodeBlock>
      <p>
        The <Code>route</Code> job runs{" "}
        <a href={HIVE_ROUTE}>
          <Code>thecomputerplumbers/hive-route</Code>
        </a>
        , which asks HiveWorker whether a machine in your hive is free and
        writes the answer to an output. Every job you want routed takes{" "}
        <Code>needs: route</Code> and reads that output in <Code>runs-on</Code>.
        Nothing else in the job changes.
      </p>

      <h2 id="inputs">Inputs and outputs</h2>
      <table>
        <thead>
          <tr>
            <th>Input</th>
            <th>Default</th>
            <th>What it is</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td>
              <Code>label</Code>
            </td>
            <td>
              <Code>{RUNNER_SIZES[0].label}</Code>
            </td>
            <td>
              The hive label to use when a machine is free. Any{" "}
              <Link href="/docs/labels-and-sizes">label and size</Link>.
            </td>
          </tr>
          <tr>
            <td>
              <Code>fallback</Code>
            </td>
            <td>
              <Code>ubuntu-latest</Code>
            </td>
            <td>
              The runner to use when none is. Set it to whatever the job runs on
              today, including a self-hosted label.
            </td>
          </tr>
          <tr>
            <td>
              <Code>wait</Code>
            </td>
            <td>
              <Code>15</Code>
            </td>
            <td>
              Seconds to keep asking before falling back, up to 120. A free
              machine ends the wait at once.
            </td>
          </tr>
        </tbody>
      </table>
      <table>
        <thead>
          <tr>
            <th>Output</th>
            <th>What it is</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td>
              <Code>runner</Code>
            </td>
            <td>
              The label to give <Code>runs-on</Code>.
            </td>
          </tr>
          <tr>
            <td>
              <Code>routed</Code>
            </td>
            <td>
              <Code>true</Code> when the job goes to your hive,{" "}
              <Code>false</Code> when it falls back.
            </td>
          </tr>
        </tbody>
      </table>

      <h2 id="what-it-checks">What it checks</h2>
      <p>
        A machine counts as free when it is connected, idle, large enough for
        the label and still enrolled in your hive. A machine that is paused,
        busy with a job for this or another hive, or revoked does not. The job
        also falls back when your organization&apos;s balance has run out, when
        the label is not one your hive serves, and when HiveWorker cannot be
        reached. The step never fails your workflow, and its log says which of
        these happened.
      </p>

      <h2 id="permissions">What it is allowed to do</h2>
      <p>
        The step needs <Code>id-token: write</Code> and nothing else. It uses
        that to ask GitHub for the job&apos;s own OIDC token, which proves to
        HiveWorker which repository is asking. It reads nothing from the
        repository. The whole action is{" "}
        <a href={HIVE_ROUTE}>one short file with no dependencies</a>; pin it to
        a commit if you want it never to change under you.
      </p>

      <h2 id="good-to-know">Good to know</h2>
      <ul>
        <li>
          <strong>Match the architecture.</strong> <Code>hive</Code> is arm64
          and <Code>ubuntu-latest</Code> is x64. If a job needs one
          architecture, fall back to <Code>ubuntu-24.04-arm</Code>, or route to
          an x64 label such as <Code>hive-x64</Code>.
        </li>
        <li>
          <strong>It checks; it does not reserve.</strong> A machine can leave
          or take another job between the check and the job being queued. The
          job then waits in GitHub&apos;s queue for a machine, and is not moved
          to the fallback.
        </li>
        <li>
          <strong>The wait starts when the route job starts.</strong> GitHub
          still has to start a hosted runner for it first.
        </li>
        <li>
          <strong>The route job is a GitHub-hosted job.</strong> It runs for a
          few seconds, and GitHub bills a private repository for it as it does
          any hosted job.
        </li>
        <li>
          <strong>More than one size.</strong> Add a second step to the route
          job with its own <Code>id</Code>, <Code>label</Code> and output, and
          read that output in the jobs that need it.
        </li>
      </ul>

      <h2 id="without-routing">Without routing</h2>
      <p>
        For a job that can wait for a machine, skip the route job and ask for
        the label directly. It stays queued at GitHub until a machine of the
        right size is free.
      </p>
      <CodeBlock title=".github/workflows/ci.yml">{plainWorkflow()}</CodeBlock>
    </>
  )
}
