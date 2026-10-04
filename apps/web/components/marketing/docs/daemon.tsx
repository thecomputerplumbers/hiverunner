import Link from "next/link"
import { Code } from "../code"

export function Daemon() {
  return (
    <>
      <p>
        The daemon is the program on a developer's Mac that takes jobs from the
        pool and runs them. It is a single signed binary with a menu bar item,
        and it runs as a per-user agent, not as root.
      </p>

      <h2 id="signing-in">Signing in</h2>
      <p>
        On first run the daemon opens a browser window where you sign in with
        GitHub and approve the machine. After that the daemon keeps one
        credential, a token for that machine alone. It can be revoked without
        affecting your other machines or your GitHub session.
      </p>

      <h2 id="states">States</h2>
      <table>
        <thead>
          <tr>
            <th>State</th>
            <th>Meaning</th>
          </tr>
        </thead>
        <tbody>
          <tr>
            <td>
              <Code>online</Code>
            </td>
            <td>Connected and ready for a job.</td>
          </tr>
          <tr>
            <td>
              <Code>busy</Code>
            </td>
            <td>Running a job.</td>
          </tr>
          <tr>
            <td>
              <Code>paused</Code>
            </td>
            <td>Connected and taking no new jobs.</td>
          </tr>
          <tr>
            <td>
              <Code>draining</Code>
            </td>
            <td>Finishing its current job and taking no more.</td>
          </tr>
          <tr>
            <td>
              <Code>offline</Code>
            </td>
            <td>Not connected.</td>
          </tr>
        </tbody>
      </table>

      <h2 id="warm-vm">The warm VM</h2>
      <p>
        The daemon keeps one VM booted and idle. When a job arrives it goes to
        that VM, so the job does not wait for a machine to boot. When the job
        ends the VM is destroyed and a fresh one is started for the next job.
      </p>

      <h2 id="your-machine">When you need your machine</h2>
      <ul>
        <li>
          When you start using the Mac during a job, the daemon throttles the VM
          and lets the job finish.
        </li>
        <li>
          Jobs and the idle VM stay available on battery. Closing the lid pauses
          new jobs and discards the idle VM.
        </li>
        <li>Pausing from the menu bar stops new jobs from arriving.</li>
      </ul>

      <h2 id="network">Network</h2>
      <p>
        The daemon makes one outbound connection to the HiveWorker coordinator
        and opens no inbound ports. Jobs talk to GitHub from inside the VM. More
        on the <Link href="/security">security page</Link>.
      </p>
    </>
  )
}
