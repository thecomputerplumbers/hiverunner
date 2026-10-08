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
      enum: ["grant", "purchase", "charge", "earning", "tax"],
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
