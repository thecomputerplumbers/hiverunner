import { eq } from "drizzle-orm"
import { user } from "@workspace/auth/schema"
import { authorize, type OperationContext } from "./context"

export async function currentUser(context: OperationContext) {
  await authorize(context, "hive:read")
  const [account] = await context.db
    .select({ id: user.id, name: user.name, email: user.email })
    .from(user)
    .where(eq(user.id, context.actor.userId))
    .limit(1)
  return {
    user: account
      ? {
          id: account.id,
          ...(!context.actor.scopes || context.actor.scopes.includes("profile")
            ? { name: account.name }
            : {}),
          ...(!context.actor.scopes || context.actor.scopes.includes("email")
            ? { email: account.email }
            : {}),
        }
      : null,
  }
}
