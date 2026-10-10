import { cn } from "@workspace/ui/lib/utils"

/**
 * Says plainly that the product is not generally available. Goes wherever a
 * visitor would otherwise expect to install or download something today.
 */
export function EarlyAccessNote({
  children,
  className,
}: {
  children?: React.ReactNode
  className?: string
}) {
  return (
    <p
      className={cn(
        "border-l-2 border-primary pl-3 text-sm leading-6 text-muted-foreground",
        className
      )}
    >
      <strong className="font-medium text-foreground">Early access.</strong>{" "}
      {children ??
        "HiveWorker is not generally available yet. These pages describe how it is designed to work."}
    </p>
  )
}
