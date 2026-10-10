import { cn } from "@workspace/ui/lib/utils"
import { Container } from "@workspace/ui-shell/components/page-header"

/** The top of an inner page: what the page is, in a title and a sentence. */
export function PageIntro({
  title,
  children,
}: {
  title: string
  children?: React.ReactNode
}) {
  return (
    <Container className="pt-14 pb-10 sm:pt-20 sm:pb-12">
      <h1 className="max-w-4xl font-heading text-3xl leading-[1.15] font-medium tracking-tight text-balance sm:text-5xl">
        {title}
      </h1>
      {children ? (
        <p className="mt-5 max-w-2xl text-lg leading-8 text-pretty text-muted-foreground">
          {children}
        </p>
      ) : null}
    </Container>
  )
}

/** A band of a page with a heading on the left and its content beside it. */
export function Band({
  title,
  children,
  className,
}: {
  title: string
  children: React.ReactNode
  className?: string
}) {
  return (
    <section className={cn("border-t border-border py-12 sm:py-16", className)}>
      <Container className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,2fr)] lg:gap-12">
        <h2 className="h-fit border-l-2 border-primary pl-4 font-heading text-xl leading-snug font-medium tracking-tight text-balance">
          {title}
        </h2>
        <div className="flex min-w-0 flex-col gap-5">{children}</div>
      </Container>
    </section>
  )
}

/**
 * Running text: paragraphs, lists, tables and headings at reading width.
 * Used by the docs and the legal pages, whose content is plain elements.
 */
export function Prose({ className, ...props }: React.ComponentProps<"div">) {
  return (
    <div
      className={cn(
        "max-w-[68ch] text-base leading-7",
        "[&_p]:my-4 [&_p]:text-pretty",
        "[&_h3]:mt-7 [&_h3]:mb-2 [&_h3]:scroll-mt-28 [&_h3]:font-medium [&_h3]:text-foreground",
        "[&_h2]:mt-10 [&_h2]:mb-3 [&_h2]:scroll-mt-28 [&_h2]:font-heading [&_h2]:text-lg [&_h2]:font-medium [&_h2]:tracking-tight",
        "[&_li]:my-1.5 [&_li]:pl-1 [&_ol]:my-4 [&_ol]:list-decimal [&_ol]:pl-5 [&_ul]:my-4 [&_ul]:list-disc [&_ul]:pl-5",
        "[&_li_a]:underline [&_li_a]:decoration-primary [&_li_a]:decoration-2 [&_li_a]:underline-offset-4 [&_p_a]:underline [&_p_a]:decoration-primary [&_p_a]:decoration-2 [&_p_a]:underline-offset-4",
        "[&_figure]:my-5",
        "[&_table]:my-5 [&_table]:w-full [&_table]:text-left [&_table]:text-sm [&_td]:border-t [&_td]:border-border [&_td]:py-2.5 [&_td]:pr-4 [&_td]:align-top [&_th]:py-2 [&_th]:pr-4 [&_th]:font-medium [&_th]:text-muted-foreground",
        "[&_dd]:mt-1.5 [&_dd]:text-muted-foreground [&_dt]:mt-6 [&_dt]:font-medium",
        className
      )}
      {...props}
    />
  )
}

/** A two-column list of short facts, or of questions and their answers. */
export function Facts({ children }: { children: React.ReactNode }) {
  return <dl className="grid gap-x-10 gap-y-6 sm:grid-cols-2">{children}</dl>
}

export function Fact({
  title,
  children,
}: {
  title: string
  children: React.ReactNode
}) {
  return (
    <div className="border-t border-border pt-4">
      <dt className="font-mono text-sm font-medium">{title}</dt>
      <dd className="mt-1.5 leading-7 text-muted-foreground">{children}</dd>
    </div>
  )
}

/** The link style for text outside `Prose`. */
export const textLink =
  "underline decoration-primary decoration-2 underline-offset-4 hover:decoration-foreground"
