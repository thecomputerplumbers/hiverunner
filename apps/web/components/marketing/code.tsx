import { cn } from "@workspace/ui/lib/utils"

/** A label, command or file name inside a sentence. */
export function Code({ className, ...props }: React.ComponentProps<"code">) {
  return (
    <code
      className={cn(
        "rounded-sm bg-muted px-1.5 py-0.5 font-mono text-[0.85em] font-medium text-foreground",
        className
      )}
      {...props}
    />
  )
}

/**
 * Something to copy or read as a file. Scrolls sideways by default; `wrap`
 * breaks a single long command instead, for narrow columns.
 */
export function CodeBlock({
  title,
  wrap = false,
  children,
  className,
}: {
  /** The file name or the shell, shown above the code. */
  title?: string
  wrap?: boolean
  children: React.ReactNode
  className?: string
}) {
  return (
    <figure
      className={cn(
        "overflow-hidden border border-white/15 bg-code text-code-foreground",
        className
      )}
    >
      {title ? (
        <figcaption className="border-b border-white/10 px-4 py-2 font-mono text-xs text-code-foreground/60">
          {title}
        </figcaption>
      ) : null}
      <pre
        tabIndex={0}
        className={cn(
          "px-4 py-3.5 font-mono text-[0.8125rem] leading-6 outline-none focus-visible:ring-3 focus-visible:ring-ring/50 focus-visible:ring-inset",
          wrap ? "break-all whitespace-pre-wrap" : "overflow-x-auto"
        )}
      >
        <code>{children}</code>
      </pre>
    </figure>
  )
}
