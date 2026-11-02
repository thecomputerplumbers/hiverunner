import { DurableObject } from "cloudflare:workers"
import { adoptListener } from "./listener-service"

/**
 * Retired. Listeners run in apps/listener now, because a long poll kept these
 * objects awake and billed. Each one still holding an alarm wakes here once,
 * hands over everything it stored, session and message cursor included, so a
 * job in flight is still settled, and then empties itself.
 *
 * Remove this class, with a deleted_classes migration, once none has an alarm.
 */
export class ScaleListener extends DurableObject<CoordinatorEnv> {
  async alarm() {
    const stored = await this.ctx.storage.list()
    const settings = stored.get("settings") as
      | { organizationId: string; label: string }
      | undefined
    try {
      if (settings)
        await adoptListener(
          this.env,
          `${settings.organizationId}:${settings.label}`,
          Object.fromEntries(stored)
        )
      await this.ctx.storage.deleteAll()
    } catch (error) {
      console.error("Listener handover retry", {
        kind: error instanceof Error ? error.name : "unknown",
      })
      await this.ctx.storage.setAlarm(Date.now() + 5000)
    }
  }
}
