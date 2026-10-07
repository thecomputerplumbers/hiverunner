import {
  RUNNER_SIZES,
  X64_RUNNER_SIZES,
  privatePoolUsdPerMinute,
} from "@workspace/pricing"
import { Code, CodeBlock } from "../code"
import { formatRate } from "../format"

export function LabelsAndSizes() {
  return (
    <>
      <p>
        A job chooses its size with its <Code>runs-on</Code> label. Each label
        is its own queue.
      </p>
      <table>
        <thead>
          <tr>
            <th>Label</th>
            <th>vCPUs</th>
            <th>Per minute, your hive</th>
          </tr>
        </thead>
        <tbody>
          {RUNNER_SIZES.map((size) => (
            <tr key={size.label}>
              <td>
                <Code>{size.label}</Code>
              </td>
              <td>{size.vcpus}</td>
              <td>{formatRate(privatePoolUsdPerMinute(size.vcpus).net)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p>
        Prices are US dollar estimates at reference speed, for jobs that run on
        your own organization's machines.
      </p>

      <h2 id="x64">Linux x64 labels</h2>
      <p>
        The labels above run arm64 jobs, on Macs and on arm64 Linux machines.
        Linux x64 jobs get labels of their own and run only on Linux x64
        machines in your hive. A job sent to one of these labels waits in
        GitHub&apos;s queue until your hive has an x64 machine free. They are
        priced by the same rule: vCPUs, minutes and the machine&apos;s speed.
      </p>
      <table>
        <thead>
          <tr>
            <th>Label</th>
            <th>vCPUs</th>
            <th>Architecture</th>
          </tr>
        </thead>
        <tbody>
          {X64_RUNNER_SIZES.map((size) => (
            <tr key={size.label}>
              <td>
                <Code>{size.label}</Code>
              </td>
              <td>{size.vcpus}</td>
              <td>Linux {size.arch}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2 id="example">In a workflow</h2>
      <CodeBlock title=".github/workflows/ci.yml">{`jobs:
  test:
    runs-on: hive
    steps:
      - uses: actions/checkout@v7
      - run: make test

  build:
    runs-on: hive-8cpu
    steps:
      - uses: actions/checkout@v7
      - run: make build`}</CodeBlock>

      <h2 id="what-runs">What a job runs on</h2>
      <ul>
        <li>
          A Linux arm64 VM. Actions, container images and binaries a job uses
          need an arm64 build. The x64 labels, once released, run a Linux x64
          VM.
        </li>
        <li>
          One job per VM. The VM is created for the job and destroyed after it.
        </li>
        <li>
          macOS, Windows and GPU jobs are not supported in the first release.
        </li>
      </ul>

      <h2 id="repositories">Which repositories can use the labels</h2>
      <p>
        The labels are created in your organization's default runner group.
        GitHub does not offer that group to public repositories unless you
        change the group's settings, which suits a hive of your own.
      </p>
    </>
  )
}
