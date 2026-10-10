import Link from "next/link"
export default function NotFound() {
  return (
    <main className="mx-auto flex max-w-lg flex-col gap-4 p-8">
      <h1 className="font-mono text-2xl font-medium tracking-tight">
        Page not found
      </h1>
      <p>This page is unavailable or you no longer have access.</p>
      <Link className="underline" href="/">
        Go to the home page
      </Link>
    </main>
  )
}
