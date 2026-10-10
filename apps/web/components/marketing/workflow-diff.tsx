import { cn } from "@workspace/ui/lib/utils"

const BEFORE = "runs-on: ubuntu-latest"
const AFTER = "runs-on: hive"

/** The whole product in two lines: the one change a workflow needs. */
export function WorkflowDiff({ className }: { className?: string }) {
  return (
    <figure
      aria-label={`Change ${BEFORE} to ${AFTER}`}
      className={cn(
        "overflow-hidden border border-code-foreground/15 bg-code font-mono text-[0.8125rem] leading-8 text-code-foreground",
        className
      )}
    >
      <pre aria-hidden="true" className="font-mono">
        <DiffLine sign="-">{BEFORE}</DiffLine>
        <DiffLine sign="+">{AFTER}</DiffLine>
      </pre>
    </figure>
  )
}

function DiffLine({ sign, children }: { sign: "+" | "-"; children: string }) {
  const added = sign === "+"
  return (
    <span
      className={cn(
        "block px-4 whitespace-pre",
        added ? "bg-diff-add/15" : "bg-diff-remove/15"
      )}
    >
      <span
        className={cn(
          "inline-block w-[2ch] select-none",
          added ? "text-diff-add" : "text-diff-remove"
        )}
      >
        {sign}
      </span>
      <span className={added ? undefined : "text-code-foreground/55"}>
        {children}
      </span>
    </span>
  )
}
