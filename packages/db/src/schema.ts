import {
  index,
  integer,
  primaryKey,
  real,
  sqliteTable,
  text,
  uniqueIndex,
} from "drizzle-orm/sqlite-core"
import { organization, user } from "@workspace/auth/schema"

/** How a hive chooses among the machines that could take a job. */
export const ROUTING_POLICIES = ["fastest", "spread", "round-robin"] as const
export type RoutingPolicy = (typeof ROUTING_POLICIES)[number]

/**
 * One row per GitHub App installation. The organization row is created from
 * it and never the other way round: a pool exists because the App was
 * installed, and for no other reason.
 */
export const githubInstallation = sqliteTable(
  "github_installation",
  {
    /**
     * GitHub's id for the account the App is installed on. The key, because a
     * reinstall gets a new installation id for the same account and must land
     * on the same organization and ledger.
     */
    accountId: integer("account_id").primaryKey(),
    installationId: integer("installation_id").notNull(),
    organizationId: text("organization_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    accountLogin: text("account_login").notNull(),
    accountType: text("account_type", {
      enum: ["Organization", "User"],
    }).notNull(),
    repositorySelection: text("repository_selection", {
      enum: ["all", "selected"],
    }).notNull(),
    status: text("status", { enum: ["active", "suspended", "removed"] })
      .notNull()
      .default("active"),
    createdAt: integer("created_at", { mode: "timestamp_ms" }).notNull(),
    updatedAt: integer("updated_at", { mode: "timestamp_ms" }).notNull(),
  },
  (table) => [
    index("github_installation_org_idx").on(table.organizationId),
    uniqueIndex("github_installation_installation_idx").on(
      table.installationId
    ),
  ]
)

export const poolSettings = sqliteTable("pool_settings", {
  organizationId: text("organization_id")
    .primaryKey()
    .references(() => organization.id, { onDelete: "cascade" }),
  /** Empty means every member of the GitHub organization may add machines. */
  allowedTeams: text("allowed_teams", { mode: "json" })
    .$type<string[]>()
    .notNull()
    .default([]),
  /** Who is paid when a member's machine runs a job. */
  earningsDestination: text("earnings_destination", {
    enum: ["organization", "developer"],
  })
    .notNull()
    .default("organization"),
  /**
   * Whether `hive-macos` jobs may run here at all. Such a job runs on a
   * member's Mac itself, as a separate user, not in a VM; an owner turns
   * this on knowing that. Off, no scale set for the label is created and
   * no Mac is offered one.
   */
  macosJobs: integer("macos_jobs", { mode: "boolean" })
    .notNull()
    .default(false),
  /**
   * Which machine a job goes to when several could take it. Read by the
   * hive's `Pool` at each offer; `pickMachine` says what each one means.
   */
  routing: text("routing", { enum: ROUTING_POLICIES })
    .notNull()
    .default("fastest"),
  version: integer("version").notNull().default(1),
  updatedAt: integer("updated_at", { mode: "timestamp_ms" }).notNull(),
})

/** GitHub's stable scale-set identity; leases and sessions live in the listener. */
export const runnerScaleSet = sqliteTable(
  "runner_scale_set",
  {
    organizationId: text("organization_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    label: text("label").notNull(),
    installationId: integer("installation_id").notNull(),
    scaleSetId: integer("scale_set_id").notNull(),
    runnerGroupId: integer("runner_group_id").notNull(),
    updatedAt: integer("updated_at", { mode: "timestamp_ms" }).notNull(),
  },
  (table) => [primaryKey({ columns: [table.organizationId, table.label] })]
)

/**
 * A developer's computer. It belongs to a person, not to a pool: which pools
 * it serves is `machinePool`. The daemon holds the machine token and nothing
 * else; only its hash is stored, so a database read cannot impersonate it.
 */
export const machine = sqliteTable(
  "machine",
  {
    id: text("id").primaryKey(),
    /** Whose machine it is. */
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    name: text("name").notNull(),
    model: text("model").notNull(),
    os: text("os").notNull(),
    arch: text("arch").notNull(),
    cores: integer("cores").notNull(),
    memoryBytes: integer("memory_bytes").notNull(),
    /**
     * Speed relative to one core of a GitHub standard hosted runner. Starts
     * from the daemon's own benchmark, which is a claim; the coordinator
     * corrects it against observed job durations.
     */
    speedScore: real("speed_score").notNull().default(1),
    /**
     * Whether the daemon says this Mac takes macOS jobs, run on the Mac
     * itself. Written from each hello, so it follows the owner's setting.
     */
    macos: integer("macos", { mode: "boolean" }).notNull().default(false),
    /**
     * Why this Mac takes no macOS jobs although its owner turned them on,
     * as the daemon says in each hello: setup it needs again, say. Null when
     * it takes them, or they are off.
     */
    macosProblem: text("macos_problem"),
    /**
     * Its owner said, in a browser, that it joins hives they newly become an
     * owner of. Never written from a machine token or by the daemon.
     */
    autoJoin: integer("auto_join", { mode: "boolean" })
      .notNull()
      .default(false),
    daemonVersion: text("daemon_version").notNull(),
    /** SHA-256 of the machine token, hex. */
    tokenHash: text("token_hash").notNull(),
    status: text("status", { enum: ["active", "revoked"] })
      .notNull()
      .default("active"),
    createdAt: integer("created_at", { mode: "timestamp_ms" }).notNull(),
    revokedAt: integer("revoked_at", { mode: "timestamp_ms" }),
  },
  (table) => [
    index("machine_user_idx").on(table.userId),
    uniqueIndex("machine_token_idx").on(table.tokenHash),
  ]
)

/**
 * A key that enrolls machines in one hive with no browser: what a fleet, or
 * an agent setting machines up, uses in place of approving each sign-in. An
 * owner or admin mints it, and every machine it enrolls is theirs, as if
 * they had signed in on it. Only the hash is kept; the key is shown once.
 */
export const enrollKey = sqliteTable(
  "enroll_key",
  {
    id: text("id").primaryKey(),
    organizationId: text("organization_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    /** Who minted it, and so whose the machines it enrolls are. */
    userId: text("user_id")
      .notNull()
      .references(() => user.id, { onDelete: "cascade" }),
    /** What it is for, in the minter's words. */
    description: text("description").notNull().default(""),
    /** SHA-256 of the key, hex. */
    keyHash: text("key_hash").notNull(),
    /** The key's first characters, to tell keys apart in a list. */
    prefix: text("prefix").notNull(),
    /** How many machines it may enroll; null for no limit. */
    maxUses: integer("max_uses"),
    uses: integer("uses").notNull().default(0),
    expiresAt: integer("expires_at", { mode: "timestamp_ms" }).notNull(),
    createdAt: integer("created_at", { mode: "timestamp_ms" }).notNull(),
    lastUsedAt: integer("last_used_at", { mode: "timestamp_ms" }),
    revokedAt: integer("revoked_at", { mode: "timestamp_ms" }),
  },
  (table) => [
    uniqueIndex("enroll_key_hash_idx").on(table.keyHash),
    index("enroll_key_org_idx").on(table.organizationId),
  ]
)

/**
 * A machine authorized to run one organization's jobs. One machine can serve
 * several organizations its owner belongs to; each is authorized, and
 * revoked, separately. The daemon holds one tunnel per row.
 */
export const machinePool = sqliteTable(
  "machine_pool",
  {
    machineId: text("machine_id")
      .notNull()
      .references(() => machine.id, { onDelete: "cascade" }),
    organizationId: text("organization_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    status: text("status", { enum: ["active", "revoked"] })
      .notNull()
      .default("active"),
    /**
     * Last state this pool's coordinator saw, written through from its
     * Durable Object so the dashboard can read it without waking the object.
     */
    presence: text("presence", {
      enum: ["offline", "paused", "online", "busy", "draining"],
    })
      .notNull()
      .default("offline"),
    lastSeenAt: integer("last_seen_at", { mode: "timestamp_ms" }),
    createdAt: integer("created_at", { mode: "timestamp_ms" }).notNull(),
    revokedAt: integer("revoked_at", { mode: "timestamp_ms" }),
  },
  (table) => [
    primaryKey({ columns: [table.machineId, table.organizationId] }),
    index("machine_pool_org_idx").on(table.organizationId, table.status),
  ]
)

/**
 * One GitHub Actions job that ran on a machine. Written when GitHub says it
 * started, priced when GitHub says it finished: the times are GitHub's, never
 * the machine's, because the machine is the one being paid.
 */
export const job = sqliteTable(
  "job",
  {
    /** `<organization>:<GitHub runner request id>`, so a replay is a no-op. */
    id: text("id").primaryKey(),
    organizationId: text("organization_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    machineId: text("machine_id")
      .notNull()
      .references(() => machine.id),
    /**
     * The coordinator's assignment id, also the runner name without `hw-`.
     * Used to attach a daemon-reported kill reason after GitHub starts the job.
     */
    assignmentId: text("assignment_id"),
    /** The `runs-on` label. */
    label: text("label").notNull(),
    repository: text("repository").notNull(),
    workflowRunId: integer("workflow_run_id"),
    githubJobId: integer("github_job_id"),
    jobName: text("job_name").notNull().default(""),
    /** Locked when the job is assigned; a later change does not reprice it. */
    vcpus: integer("vcpus").notNull(),
    speedScore: real("speed_score").notNull(),
    /** Micro-tokens per compute unit. */
    rate: integer("rate").notNull(),
    startedAt: integer("started_at", { mode: "timestamp_ms" }).notNull(),
    completedAt: integer("completed_at", { mode: "timestamp_ms" }),
    /** GitHub's conclusion: `succeeded`, `failed`, `canceled`. */
    result: text("result"),
    /**
     * Why the runner or VM was stopped, when that was not a normal finish:
     * `guest_oom`, `host_killed`, `disk_full`, `timeout`, `daemon_shutdown`,
     * `vm_crash`, `released`. Written from the daemon's `job.exited`.
     */
    endReason: text("end_reason"),
    /** One sentence a human can read, with the host and the numbers. */
    endDetail: text("end_detail"),
    computeUnits: real("compute_units"),
    /** Micro-tokens. `amount` is what the org paid; `tax + earning` equals it. */
    amount: integer("amount"),
    tax: integer("tax"),
    earning: integer("earning"),
    payeeType: text("payee_type", { enum: ["organization", "user"] }),
    payeeId: text("payee_id"),
    /** The exact bytes that were signed, and the signature over them. */
    receipt: text("receipt"),
    receiptSignature: text("receipt_signature"),
    receiptKeyId: text("receipt_key_id"),
  },
  (table) => [
    index("job_org_idx").on(table.organizationId, table.startedAt),
    index("job_machine_idx").on(table.machineId, table.startedAt),
    index("job_assignment_idx").on(table.assignmentId),
  ]
)

/**
 * What the daemon said about why a runner stopped. Written on `job.exited`,
 * which can arrive before GitHub has started the job row. `applyJobExit`
 * copies it onto `job` once that row exists.
 */
export const jobExit = sqliteTable(
  "job_exit",
  {
    assignmentId: text("assignment_id").primaryKey(),
    organizationId: text("organization_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    machineId: text("machine_id")
      .notNull()
      .references(() => machine.id),
    exitCode: integer("exit_code").notNull(),
    reason: text("reason").notNull().default(""),
    detail: text("detail").notNull().default(""),
    createdAt: integer("created_at", { mode: "timestamp_ms" }).notNull(),
  },
  (table) => [
    index("job_exit_org_idx").on(table.organizationId, table.createdAt),
  ]
)

/**
 * Append-only. A balance is the sum of an account's entries; nothing stores a
 * mutable balance, so there is nothing to drift.
 *
 * Amounts are signed micro-tokens: positive credits the account.
 */
export const ledgerEntry = sqliteTable(
  "ledger_entry",
  {
    /**
     * Deterministic where the entry must happen once: `grant:<org>` cannot be
     * inserted twice however many times the install webhook is delivered.
     */
    id: text("id").primaryKey(),
    accountType: text("account_type", {
      enum: ["organization", "user", "treasury"],
    }).notNull(),
    /** Organization or user id; `treasury` for the treasury. */
    accountId: text("account_id").notNull(),
    kind: text("kind", {
      enum: ["grant", "purchase", "gift", "charge", "earning", "tax"],
    }).notNull(),
    amount: integer("amount").notNull(),
    jobId: text("job_id"),
    createdAt: integer("created_at", { mode: "timestamp_ms" }).notNull(),
  },
  (table) => [
    index("ledger_entry_account_idx").on(
      table.accountType,
      table.accountId,
      table.createdAt
    ),
  ]
)

/**
 * An organization's card on file and its standing instruction to keep the
 * balance up. One row per organization, created the first time it pays by
 * card.
 */
export const autoTopUp = sqliteTable("auto_top_up", {
  organizationId: text("organization_id")
    .primaryKey()
    .references(() => organization.id, { onDelete: "cascade" }),
  /** Off unless an owner or admin asked for it. Turned off by a failure. */
  enabled: integer("enabled", { mode: "boolean" }).notNull().default(false),
  /** What is charged each time, in cents. */
  amountCents: integer("amount_cents").notNull(),
  /** The balance, in cents, below which the card is charged. */
  belowCents: integer("below_cents").notNull().default(500),
  /**
   * The most automatic charges may add up to in a calendar month, in cents.
   * Null is no ceiling.
   */
  monthlyCapCents: integer("monthly_cap_cents"),
  /** The card processor's id for the saved card. Not a card number. */
  paymentMethodId: text("payment_method_id"),
  /** For "Visa ending 4242": what an owner needs to recognise the card. */
  cardBrand: text("card_brand"),
  cardLast4: text("card_last4"),
  /**
   * Set while a charge is in flight, so the next sweep does not start a
   * second. Cleared when the charge is credited or fails.
   */
  pendingSince: integer("pending_since", { mode: "timestamp_ms" }),
  /** Why the last automatic charge failed, in words an owner can act on. */
  lastError: text("last_error"),
  updatedAt: integer("updated_at", { mode: "timestamp_ms" }).notNull(),
})

/** GitHub redelivers. The delivery id makes handling a webhook idempotent. */
export const webhookDelivery = sqliteTable("webhook_delivery", {
  id: text("id").primaryKey(),
  event: text("event").notNull(),
  receivedAt: integer("received_at", { mode: "timestamp_ms" }).notNull(),
})

/**
 * The most a hive means to spend on jobs in a calendar month, and what
 * happens when it has. No row is no budget.
 */
export const spendBudget = sqliteTable("spend_budget", {
  organizationId: text("organization_id")
    .primaryKey()
    .references(() => organization.id, { onDelete: "cascade" }),
  monthlyCents: integer("monthly_cents").notNull(),
  /**
   * `warn` tells the owners and carries on. `pause` also stops new jobs
   * being acquired until the month turns or the budget is raised; a job
   * already running is never cut off.
   */
  atLimit: text("at_limit", { enum: ["warn", "pause"] })
    .notNull()
    .default("warn"),
  updatedAt: integer("updated_at", { mode: "timestamp_ms" }).notNull(),
})

/**
 * A notice about money that has been sent, so it is sent once. The id names
 * the condition: `low-balance:<org>` while the balance is low, or
 * `budget-reached:<org>:2026-10` for one month's budget. A row for a
 * condition that has passed is removed, so the next time is told again.
 */
export const billingNotice = sqliteTable(
  "billing_notice",
  {
    id: text("id").primaryKey(),
    organizationId: text("organization_id")
      .notNull()
      .references(() => organization.id, { onDelete: "cascade" }),
    kind: text("kind").notNull(),
    createdAt: integer("created_at", { mode: "timestamp_ms" }).notNull(),
  },
  (table) => [index("billing_notice_org_idx").on(table.organizationId)]
)
