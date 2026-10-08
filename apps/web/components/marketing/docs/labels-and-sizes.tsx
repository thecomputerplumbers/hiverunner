import {
  MACOS_RUNNER_SIZES,
  RUNNER_SIZES,
  X64_RUNNER_SIZES,
  competitorUsdPerMinuteForLabel,
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

      <h2 id="macos">macOS labels</h2>
      <p>
        For Xcode: iOS builds, signing, simulators. A macOS job does not run in
        a VM. It runs on a member&apos;s Mac itself, as a separate local user
        named <Code>hiveworker</Code> that cannot read the owner&apos;s files or
        keychain, but can use the Xcode and other software installed on that
        Mac, and its network. Two people have to agree before one runs: the
        Mac&apos;s owner, with <Code>hived macos setup</Code>, and an owner of
        your hive, in its settings. Jobs sent to the label wait in GitHub&apos;s
        queue until a Mac that has both is free.
      </p>
      <table>
        <thead>
          <tr>
            <th>Label</th>
            <th>Billed as</th>
            <th>Per minute, your hive</th>
            <th>GitHub-hosted</th>
          </tr>
        </thead>
        <tbody>
          {MACOS_RUNNER_SIZES.map((size) => (
            <tr key={size.label}>
              <td>
                <Code>{size.label}</Code>
              </td>
              <td>{size.vcpus} vCPUs</td>
              <td>{formatRate(privatePoolUsdPerMinute(size.vcpus).net)}</td>
              <td>
                {formatRate(
                  competitorUsdPerMinuteForLabel("github", size.label) ?? 0
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <p>
        The job has the whole Mac, less two cores kept for its owner, and is
        billed as four vCPUs: the size GitHub&apos;s standard macOS runner is.
        Caches the job leaves in the <Code>hiveworker</Code> user&apos;s home,
        such as CocoaPods and npm, stay for the next job; the runner&apos;s
        working directory does not. The Mac&apos;s owner installs what every job
        needs, typically CocoaPods and Fastlane with Homebrew; a job installs
        the rest with <Code>setup-*</Code> actions, as on Linux.
      </p>
      <p>An Expo app built with EAS, on your own Mac:</p>
      <CodeBlock title=".github/workflows/ios.yml">{`jobs:
  ios:
    runs-on: hive-macos
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-node@v4
        with:
          node-version: 22
      - run: npm ci
      - run: npx eas-cli build --local --platform ios --profile preview --non-interactive --output app.ipa
        env:
          EXPO_TOKEN: \${{ secrets.EXPO_TOKEN }}
      - uses: actions/upload-artifact@v4
        with:
          name: ios
          path: app.ipa`}</CodeBlock>
      <p>
        Android builds need no Mac:{" "}
        <Code>eas build --local --platform android</Code> runs on the Linux
        labels with <Code>setup-java</Code> and{" "}
        <Code>android-actions/setup-android</Code>.
      </p>

      <h2 id="example">In a workflow</h2>
      <CodeBlock title=".github/workflows/ci.yml">{`jobs:
  test:
    runs-on: hive
    steps:
      - uses: actions/checkout@v7
      - uses: oven-sh/setup-bun@v2
      - run: bun test

  build:
    runs-on: hive-8cpu
    steps:
      - uses: actions/checkout@v7
      - uses: oven-sh/setup-bun@v2
      - run: bun run build`}</CodeBlock>

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
          <Code>hive-macos</Code> jobs run on a Mac itself, as a separate user,
          with that Mac&apos;s Xcode. See <a href="#macos">macOS labels</a>.
        </li>
        <li>Windows and GPU jobs are not supported.</li>
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
