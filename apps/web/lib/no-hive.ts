import { env } from "cloudflare:workers"

/**
 * What to tell someone who signed in fine but belongs to no hive. A hive is
 * a GitHub organization with the App installed, so the way out is to install
 * it or to be let into one; nothing on this side can be retried.
 */
export function noHiveMessage() {
  return (
    "Your GitHub account is not in a hive yet. A hive is a GitHub " +
    "organization with HiveWorker installed. Install it on yours at " +
    `${env.APP_URL}/install, or ask a hive owner for their invite link, ` +
    "then run hived login again."
  )
}
