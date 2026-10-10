import type { ComponentProps, ReactNode } from "react"
import { cn } from "@workspace/ui/lib/utils"

/**
 * A titled pane of the console, drawn like a terminal split: a hairline box
 * with its name set into the top edge.
 */
export function Panel({
  title,
  aside,
  children,
  className,
  bodyClassName,
  flush,
  ...props
}: Omit<ComponentProps<"section">, "title"> & {
  title: ReactNode
  aside?: ReactNode
  bodyClassName?: string
  /** No padding: the body is rows that run edge to edge and pad themselves. */
  flush?: boolean
}) {
  return (
    <section
      className={cn(
        "flex min-w-0 flex-col border border-border bg-card/50",
        className
      )}
      {...props}
    >
      <header className="flex min-h-12 items-center justify-between gap-3 border-b border-border px-4 font-mono text-xs sm:px-6">
        <h2 className="flex items-center gap-2.5 text-[0.8125rem] font-normal">
          <span aria-hidden="true" className="h-2.5 w-2 bg-primary hex" />
          {title}
        </h2>
        {aside ? (
          <div className="flex items-center gap-3 text-muted-foreground">
            {aside}
          </div>
        ) : null}
      </header>
      <div className={cn("flex-1", !flush && "p-4 sm:p-6", bodyClassName)}>
        {children}
      </div>
    </section>
  )
}

/** A figure with its label, and room under it for a line or a small chart. */
export function Stat({
  label,
  value,
  children,
  className,
}: {
  label: string
  value: ReactNode
  children?: ReactNode
  className?: string
}) {
  return (
    <div className={cn("flex min-w-0 flex-col gap-1 p-4", className)}>
      <p className="font-mono text-xs text-muted-foreground">{label}</p>
      <p className="font-mono text-2xl font-medium tracking-tight tabular-nums">
        {value}
      </p>
      {children ? (
        <div className="text-sm text-muted-foreground">{children}</div>
      ) : null}
    </div>
  )
}

export function Notice({
  tone = "info",
  children,
}: {
  tone?: "info" | "alert"
  children: ReactNode
}) {
  return (
    <p
      role={tone === "alert" ? "alert" : "status"}
      className={cn(
        "border-l-2 bg-card/50 px-4 py-3 text-sm",
        tone === "alert" ? "border-destructive" : "border-primary"
      )}
    >
      {children}
    </p>
  )
}

/** A shell command or a snippet, on the dark code surface in both themes. */
export function Snippet({
  children,
  prompt,
  className,
}: {
  children: string
  prompt?: boolean
  className?: string
}) {
  return (
    <pre
      className={cn(
        "overflow-x-auto bg-code px-4 py-3 font-mono text-xs leading-6 text-code-foreground",
        className
      )}
    >
      <code>
        {prompt ? (
          <span aria-hidden="true" className="text-primary select-none">
            ${" "}
          </span>
        ) : null}
        {children}
      </code>
    </pre>
  )
}
