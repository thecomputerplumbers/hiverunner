import { cn } from "@workspace/ui/lib/utils"

/**
 * Where a routed job ends up: on the pool when a machine is free, on the
 * GitHub-hosted runner when none is. Uses no `p`, so it sits inside `Prose`.
 */
export function RouteDiagram({ className }: { className?: string }) {
  return (
    <figure
      className={cn(
        "grid items-stretch gap-y-3 text-sm md:grid-cols-[minmax(0,4fr)_minmax(0,5fr)]",
        className
      )}
    >
      <figcaption className="sr-only">
        The route job checks for a free machine for up to 15 seconds. If one is
        free, the job runs on your pool. If none is, it runs on the
        GitHub-hosted runner.
      </figcaption>
      <div className="flex flex-col justify-center gap-1 border border-border bg-card p-3 sm:p-4">
        <span className="font-mono font-medium">route</span>
        <span className="text-muted-foreground">
          Is a machine free? Waits up to 15 s.
        </span>
      </div>
      <div className="flex flex-col gap-3">
        <Outcome when="Yes" runner="hive" where="Your pool" solid />
        <Outcome
          when="No"
          runner="ubuntu-latest"
          where="GitHub-hosted, as before"
        />
      </div>
    </figure>
  )
}

function Outcome({
  when,
  runner,
  where,
  solid = false,
}: {
  when: string
  runner: string
  where: string
  solid?: boolean
}) {
  return (
    <div className="flex items-center">
      <div
        aria-hidden="true"
        className={cn(
          "hidden w-8 shrink-0 border-t-2 md:block lg:w-12",
          solid ? "border-primary" : "border-dashed border-muted-foreground/50"
        )}
      />
      <div
        className={cn(
          "flex flex-1 items-baseline gap-3 border p-3",
          solid
            ? "border-2 border-primary bg-primary/10"
            : "border-dashed border-border"
        )}
      >
        <span className="w-8 shrink-0 font-medium">{when}</span>
        <span className="flex flex-col gap-0.5">
          <span className="font-mono font-medium">{runner}</span>
          <span className="text-muted-foreground">{where}</span>
        </span>
      </div>
    </div>
  )
}
