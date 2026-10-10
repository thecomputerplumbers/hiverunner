import { RUNNER_SIZES, privatePoolUsdPerMinute } from "@workspace/pricing"

/** Every `runs-on` label a workflow can ask for, and what a minute costs. */
export function RunnerSizes() {
  return (
    <table className="h-fit w-full font-mono text-xs">
      <thead>
        <tr className="text-left text-muted-foreground [&>th]:pb-2 [&>th]:font-normal">
          <th>runs-on</th>
          <th>vCPUs</th>
          <th className="text-right">per minute</th>
        </tr>
      </thead>
      <tbody>
        {RUNNER_SIZES.map((runner) => (
          <tr key={runner.label} className="border-t border-border [&>td]:py-2">
            <td className="text-primary">{runner.label}</td>
            <td>{runner.vcpus}</td>
            <td className="text-right tabular-nums">
              ${privatePoolUsdPerMinute(runner.vcpus).net.toFixed(5)}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}
