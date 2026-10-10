import Link from "next/link"
import { notFound } from "next/navigation"
import { DOCS, findDoc } from "@/components/marketing/docs"
import { Prose } from "@/components/marketing/page-parts"

export const dynamicParams = false

export function generateStaticParams() {
  return DOCS.map((doc) => ({ slug: doc.slug }))
}

export async function generateMetadata({
  params,
}: {
  params: Promise<{ slug: string }>
}) {
  const doc = findDoc((await params).slug)
  return doc ? { title: doc.title, description: doc.description } : {}
}

export default async function DocPage({
  params,
}: {
  params: Promise<{ slug: string }>
}) {
  const doc = findDoc((await params).slug)
  if (!doc) notFound()
  const index = DOCS.indexOf(doc)
  const next = DOCS[index + 1]
  return (
    <article>
      <h1 className="font-heading text-4xl leading-[1.05] font-medium tracking-tight">
        {doc.title}
      </h1>
      <Prose className="mt-4">
        <doc.Body />
      </Prose>
      {next ? (
        <p className="mt-12 max-w-[68ch] border-t border-border pt-6 text-muted-foreground">
          Next:{" "}
          <Link
            href={`/docs/${next.slug}`}
            className="font-medium text-foreground underline decoration-primary decoration-2 underline-offset-4"
          >
            {next.title}
          </Link>
        </p>
      ) : null}
    </article>
  )
}
