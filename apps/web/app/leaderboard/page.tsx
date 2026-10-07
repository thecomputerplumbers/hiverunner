import { getLeaderboard } from "@workspace/db/leaderboard"
import { getLiveSnapshot } from "@workspace/db/live"

import { AccountPage } from "@/components/account-page"
import { getDb } from "@/db"
import { requirePool } from "@/lib/session"
import { Board } from "./board"

export const metadata = { title: "Leaderboard" }

export default async function LeaderboardPage() {
  const { organizationId, installation, session } = await requirePool()
  const db = getDb()
  const [live, board] = await Promise.all([
    getLiveSnapshot(db, organizationId),
    getLeaderboard(db, organizationId),
  ])
  return (
    <AccountPage title="leaderboard" live={live}>
      <Board
        people={board.people}
        machines={board.machines}
        organization={installation.accountLogin}
        userId={session.user.id}
      />
    </AccountPage>
  )
}
