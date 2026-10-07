"use client"

import * as React from "react"
import Link from "next/link"
import { Button } from "@workspace/ui/components/button"

import { ActionButton } from "@/components/action-button"
import { CombLegend, PoolComb } from "@/components/console/comb"
import { CopyButton, parallelJobs } from "@/components/console/invite"
import { useLive } from "@/components/console/live"
import { Panel, Snippet } from "@/components/console/panel"

/**
 * The pool, with one cell held open for the person reading. The page keeps
 * watching: the moment their Mac connects, the cell fills and the page says
 * so, without them coming back to press anything.
 */
export function JoinPool({
  organizationId,
  login,
  userId,
  command,
  elsewhere,
}: {
  organizationId: string
  login: string
  userId: string
  command: string
  elsewhere: Array<{ id: string; name: string; model: string }>
}) {
  const { snapshot } = useLive()
  if (!snapshot) return null
  const mine = snapshot.machines.filter((machine) => machine.userId === userId)
  const joined = mine.length > 0
  const others = snapshot.machines.length - mine.length
  const slots = parallelJobs(snapshot.machines)

  return (
    <>
      <div className="flex max-w-3xl flex-col gap-3">
        <h1 className="font-mono text-3xl leading-tight font-medium tracking-tight text-balance sm:text-4xl">
          {joined
            ? `You're in. ${mine[0]!.name} has a cell in ${login}.`
            : `${login} has a cell waiting for your Mac.`}
        </h1>
        <p className="text-lg text-muted-foreground">
          {joined
            ? "It takes CI jobs when it has cores to spare and steps aside when you need them. There is nothing else to do."
            : `${login} runs its GitHub Actions on its own developers' Macs instead of renting runners. ${
                others
                  ? `${others} ${others === 1 ? "is" : "are"} in already.`
                  : "Yours would be the first."
              } Adding yours takes one command.`}
        </p>
      </div>

      <div className="grid gap-4 lg:grid-cols-5">
        <Panel
          title={`${login} pool`}
          aside={
            <span className="flex items-center gap-1.5">
              <span
                aria-hidden="true"
                className="size-1.5 rounded-full bg-primary motion-safe:animate-pulse"
              />
              live
            </span>
          }
          className="lg:col-span-3"
          bodyClassName="flex flex-col gap-4"
        >
          <PoolComb
            machines={snapshot.machines}
            running={snapshot.running}
            seat={joined ? undefined : { label: "yours" }}
          />
          <CombLegend machines={snapshot.machines} />
          <p className="font-mono text-xs text-muted-foreground">
            {slots} jobs at once across {snapshot.machines.length}{" "}
            {snapshot.machines.length === 1 ? "Mac" : "Macs"}
            {snapshot.running.length
              ? `, ${snapshot.running.length} running right now`
              : ""}
          </p>
        </Panel>

        {joined ? (
          <Panel
            title="done"
            className="border-primary/60 lg:col-span-2"
            bodyClassName="flex flex-col gap-4"
          >
            <ul className="flex flex-col gap-2 font-mono text-sm">
              {mine.map((machine) => (
                <li key={machine.id} className="flex items-center gap-2">
                  <span
                    aria-hidden="true"
                    className="flex size-5 items-center justify-center bg-primary text-xs text-primary-foreground"
                  >
                    ✓
                  </span>
                  {machine.name}
                  <span className="text-muted-foreground">
                    {machine.cores} cores
                  </span>
                </li>
              ))}
            </ul>
            <p className="text-sm text-muted-foreground">
              Run <code className="text-foreground">hived status</code> in
              Terminal to see what it is doing, and{" "}
              <code className="text-foreground">hived pause</code> whenever you
              want the whole machine back.
            </p>
            <div>
              <Button render={<Link href="/dashboard" />}>See the pool</Button>
            </div>
          </Panel>
        ) : (
          <Panel
            title="add your Mac"
            className="border-primary/60 lg:col-span-2"
            bodyClassName="flex flex-col gap-4"
          >
            <ol className="flex flex-col gap-4 text-sm">
              <li className="flex flex-col gap-2">
                <p>
                  <Step n={1} /> Paste this into Terminal on your Apple Silicon
                  Mac.
                </p>
                <Snippet prompt>{command}</Snippet>
                <div>
                  <CopyButton text={command}>Copy the command</CopyButton>
                </div>
              </li>
              <li>
                <Step n={2} /> A browser tab asks you to confirm with GitHub. No
                password, no card.
              </li>
              <li className="flex items-start gap-2">
                <span>
                  <Step n={3} /> Leave this page open.
                </span>
                <span className="font-mono text-xs text-primary motion-safe:animate-pulse">
                  watching for your Mac…
                </span>
              </li>
            </ol>
            {elsewhere.length > 0 && (
              <div className="flex flex-col gap-2 border-t border-border pt-3">
                <p className="text-sm text-muted-foreground">
                  Already running HiveWorker for another organization? Nothing
                  to install:
                </p>
                {elsewhere.map((machine) => (
                  <ActionButton
                    key={machine.id}
                    label={`Add ${machine.name} to ${login}`}
                    path="/api/machines/pools"
                    body={{ machineId: machine.id, organizationId }}
                  />
                ))}
              </div>
            )}
          </Panel>
        )}
      </div>

      {!joined && (
        <dl className="grid gap-px border border-border bg-border text-sm sm:grid-cols-3 [&_dd]:text-muted-foreground [&_dt]:mb-1 [&_dt]:font-mono [&_dt]:text-xs [&_dt]:font-medium [&>div]:bg-background [&>div]:p-4">
          <div>
            <dt>your cores come first</dt>
            <dd>
              Two cores always stay yours, and you set how much of the rest it
              may use. <code>hived pause</code> stops it at once.
            </dd>
          </div>
          <div>
            <dt>jobs never touch your files</dt>
            <dd>
              Each job runs in its own Linux VM that is thrown away when the job
              ends. It cannot see your disk.
            </dd>
          </div>
          <div>
            <dt>nothing to pay or manage</dt>
            <dd>
              {login} is already set up. You are lending spare compute, and you
              can take it back with <code>hived uninstall</code>.
            </dd>
          </div>
        </dl>
      )}
    </>
  )
}

const Step = ({ n }: { n: number }) => (
  <span
    aria-hidden="true"
    className="mr-1.5 inline-flex size-5 items-center justify-center border border-border font-mono text-xs"
  >
    {n}
  </span>
)
