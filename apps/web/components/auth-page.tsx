import type { ReactNode } from "react"
import {
  Card,
  CardHeader,
  CardTitle,
  CardContent,
} from "@workspace/ui/components/card"
export function AuthPage({
  title,
  children,
  wide = false,
}: {
  title: string
  children: ReactNode
  /** Room for instructions rather than a single form. */
  wide?: boolean
}) {
  return (
    <main className="flex min-h-screen items-center justify-center p-6">
      <Card className={wide ? "w-full max-w-2xl" : "w-full max-w-md"}>
        <CardHeader>
          <CardTitle>{title}</CardTitle>
        </CardHeader>
        <CardContent>{children}</CardContent>
      </Card>
    </main>
  )
}
