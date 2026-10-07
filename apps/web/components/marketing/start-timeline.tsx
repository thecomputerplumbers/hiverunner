import { cn } from "@workspace/ui/lib/utils"

/**
 * Why a job starts sooner: the VM it needs is already booted. The segments
 * show which steps happen, in order. Their widths are not measurements.
 */
export function StartTimeline({ className }: { className?: string }) {
  return (
    <figure className={cn("flex flex-col gap-6", className)}>
      <Track label="A runner provisioned on demand">
        <Segment className="sm:flex-2">Queued</Segment>
        <Segment className="sm:flex-3">Provision a VM</Segment>
        <Segment className="sm:flex-2">Boot</Segment>
        <Segment className="sm:flex-5" job>
          Your job
        </Segment>
      </Track>
      <Track label="A Mac in your pool">
        <Segment className="sm:flex-2">Queued</Segment>
        <Segment className="sm:flex-5" job>
          Your job
        </Segment>
        <span aria-hidden="true" className="hidden sm:block sm:flex-5" />
      </Track>
      <figcaption className="text-sm leading-6 text-muted-foreground">
        The order of steps, not a benchmark. Each Mac keeps one VM booted, so
        there is nothing to provision when a job arrives.
      </figcaption>
    </figure>
  )
}

function Track({
  label,
  children,
}: {
  label: string
  children: React.ReactNode
}) {
  return (
    <div className="flex flex-col gap-2">
      <p className="font-mono text-sm">{label}</p>
      <div className="flex flex-col gap-1 sm:flex-row">{children}</div>
    </div>
  )
}

function Segment({
  job = false,
  className,
  children,
}: {
  job?: boolean
  className?: string
  children: React.ReactNode
}) {
  return (
    <span
      className={cn(
        "flex h-11 min-w-0 items-center px-3 font-mono text-xs whitespace-nowrap",
        job
          ? "bg-primary font-medium text-primary-foreground"
          : "border border-dashed border-foreground/30 text-muted-foreground",
        className
      )}
    >
      {children}
    </span>
  )
}
