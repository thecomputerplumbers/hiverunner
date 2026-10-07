import { cn } from "@workspace/ui/lib/utils"

/**
 * Where a job runs and who it talks to. Three boundaries: the VM inside the
 * Mac, the job's own connection to GitHub, and the daemon's connection to the
 * coordinator, which carries assignments and no job data.
 */
export function IsolationDiagram({ className }: { className?: string }) {
  return (
    <figure
      className={cn(
        "grid items-start gap-x-0 gap-y-3 text-sm md:grid-cols-[minmax(0,5fr)_minmax(0,4fr)]",
        className
      )}
    >
      <figcaption className="sr-only">
        A job runs in a throwaway VM on a developer's Mac. The VM talks to
        GitHub directly. The daemon talks to the HiveWorker coordinator, which
        assigns jobs and never carries job data.
      </figcaption>
      <div className="flex flex-col gap-3 border border-border bg-card p-3 sm:p-4">
        <p className="font-mono font-medium">A developer's Mac</p>
        <div className="flex min-h-28 flex-col justify-between gap-3 border-2 border-primary bg-primary/10 p-3">
          <p className="font-medium">Throwaway Linux VM</p>
          <p className="w-fit bg-code px-2.5 py-1.5 font-mono text-xs text-code-foreground">
            GitHub's runner, one job, then destroyed
          </p>
        </div>
        <div className="flex min-h-16 flex-col justify-center border border-dashed border-border p-3">
          <p className="font-medium">HiveWorker daemon</p>
          <p className="text-muted-foreground">
            Runs as your user, not root. No inbound ports.
          </p>
        </div>
        <p className="text-muted-foreground">
          The Mac's files and keychain stay outside the VM.
        </p>
      </div>
      <div className="flex flex-col gap-3 md:py-4">
        <div aria-hidden="true" className="hidden h-6 md:block" />
        <Link
          className="md:min-h-28"
          carries="The job, its logs and its artifacts"
          to="GitHub"
          solid
        />
        <Link
          className="md:min-h-16"
          carries="Presence and job assignment. No job data"
          to="HiveWorker coordinator"
        />
      </div>
    </figure>
  )
}

function Link({
  carries,
  to,
  solid = false,
  className,
}: {
  carries: string
  to: string
  solid?: boolean
  className?: string
}) {
  return (
    <div className={cn("flex items-center", className)}>
      <div
        aria-hidden="true"
        className={cn(
          "hidden w-8 shrink-0 border-t-2 md:block lg:w-14",
          solid ? "border-primary" : "border-dashed border-muted-foreground/50"
        )}
      />
      <div
        className={cn(
          "flex-1 border p-3",
          solid ? "border-primary" : "border-dashed border-border"
        )}
      >
        <p className="font-medium">{to}</p>
        <p className="text-muted-foreground">{carries}</p>
      </div>
    </div>
  )
}
