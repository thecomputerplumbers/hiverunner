import { cn } from "@workspace/ui/lib/utils"

/**
 * The marker before a step in a sequence: a cell of comb that fills as the
 * step is done. Decorative; say the state in words beside it.
 */
export function HexMark({
  n,
  state = "todo",
  size = "md",
  className,
}: {
  /** The step's place in its sequence, counted from one. */
  n: number
  /** `next` is the one step to do now. */
  state?: "done" | "next" | "todo"
  size?: "sm" | "md"
  className?: string
}) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        "inline-flex shrink-0 items-center justify-center font-mono hex",
        size === "sm" ? "h-6 w-[1.3125rem] text-xs" : "h-8 w-7 text-sm",
        state === "done"
          ? "bg-primary font-semibold text-primary-foreground"
          : state === "next"
            ? "bg-primary/25 text-primary"
            : "bg-muted text-muted-foreground",
        className
      )}
    >
      {state === "done" ? "✓" : n}
    </span>
  )
}
