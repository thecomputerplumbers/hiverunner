import Link from "next/link"
import { DOCS } from "@/components/marketing/docs"

export const metadata = {
  title: "Docs",
  description:
    "How to set up HiveWorker and how it works: quickstart, labels and sizes, the daemon, hives, pricing and FAQ.",
}

export default function DocsIndexPage() {
  return (
    <>
      <h1 className="font-heading text-4xl leading-[1.05] font-medium tracking-tight">
        Docs
      </h1>
      <p className="mt-4 max-w-2xl text-lg leading-8 text-muted-foreground">
        How to set HiveWorker up and how each part behaves.
      </p>
      <ul className="mt-8 max-w-2xl border-b border-border">
        {DOCS.map((doc) => (
          <li key={doc.slug} className="border-t border-border">
            <Link
              href={`/docs/${doc.slug}`}
              className="group flex flex-col gap-1 py-4 outline-none focus-visible:ring-3 focus-visible:ring-ring/50"
            >
              <span className="font-heading text-lg font-normal underline decoration-transparent decoration-2 underline-offset-4 group-hover:decoration-primary">
                {doc.title}
              </span>
              <span className="text-muted-foreground">{doc.description}</span>
            </Link>
          </li>
        ))}
      </ul>
    </>
  )
}
