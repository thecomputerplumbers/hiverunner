"use client"
import { Button } from "@workspace/ui/components/button"
import product from "@/config/product.json"
export default function ErrorPage({
  reset,
}: {
  error: Error & { digest?: string }
  reset: () => void
}) {
  return (
    <main className="mx-auto flex max-w-lg flex-col gap-4 p-8">
      <h1 className="font-mono text-2xl font-medium tracking-tight">
        Something went wrong
      </h1>
      <p>
        Please try again. If this continues, text{" "}
        <a className="underline" href={`sms:${product.supportSms}`}>
          {product.supportSmsDisplay}
        </a>
        .
      </p>
      <Button onClick={reset}>Try again</Button>
    </main>
  )
}
