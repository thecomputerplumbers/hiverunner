import Link from "next/link"
import { Code } from "../code"

export function Daemon() {
  return (
    <>
      <p>
        The daemon is the program on a developer's Mac that takes jobs from the
        hive and runs them. It is a single signed binary, and it runs as a
        per-user agent, not as root. A menu bar item is planned.
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
          Each job leaves two CPU cores for the owner. Automatic throttling when
          you start using the Mac is planned.
        </li>
        <li>
          Jobs and the idle VM stay available on battery. Closing the lid pauses
          new jobs and discards the idle VM. An active job can stall if the Mac
          goes to sleep.
        </li>
        <li>
          When the machine is not taking jobs, <Code>hived status</Code> says
          why and what to do about it.
        </li>
        <li>A menu bar control to pause new jobs is planned.</li>
      </ul>

      <h2 id="unattended">A machine nobody sits at</h2>
      <p>
        To take jobs with the lid closed, run{" "}
        <Code>hived config set lid on</Code>. A closed MacBook sleeps unless
        sleep is disabled, and a sleeping Mac takes no jobs, so also run{" "}
        <Code>sudo pmset -a disablesleep 1</Code>. If you have not,{" "}
        <Code>hived status</Code> says so.
      </p>
      <p>
        <Code>hived doctor</Code> checks what would stop the machine taking jobs
        with its lid closed or after a restart: sleep settings, power,
        auto-login and FileVault. It says how to fix each one.
      </p>

      <h2 id="network">Network</h2>
      <p>
        The daemon makes one outbound connection to the HiveWorker coordinator
        and opens no inbound ports. Jobs talk to GitHub from inside the VM. More
        on the <Link href="/security">security page</Link>.
      </p>
    </>
  )
}
