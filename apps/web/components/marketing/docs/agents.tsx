import Link from "next/link"
import { RUNNER_SIZES } from "@workspace/pricing"
import { journey } from "@/mcp/journey"
import { HIVE_TOOLS } from "@/mcp/tool-list"
import { Code, CodeBlock } from "../code"
import { MCP_URL, mcpClients, PRODUCT_ORIGIN } from "../site"

const clients = mcpClients()

/** The shared steps mark commands with backticks; here they are set as code. */
const coded = (text: string) =>
  text
    .split("`")
    .map((part, index) => (index % 2 ? <Code key={index}>{part}</Code> : part))

export function Agents() {
  return (
    <>
      <p>
        HiveWorker has an MCP server. Point Claude Code, Cursor, Codex or any
        MCP client at one URL and it can read your hive, write the workflow
        change and tell you what a month would cost.
      </p>
      <CodeBlock title="MCP endpoint" wrap>
        {MCP_URL}
      </CodeBlock>
      <p>
        There is no API key to paste. The client registers itself and you sign
        in with GitHub in a browser.
      </p>

      <h2 id="from-nothing">From nothing to a first job</h2>
      <p>
        Each state you can be in, who acts, and how the agent can tell. An agent
        does everything except four things only a person can: install the GitHub
        App, run the install command on a machine, add funds and approve the
        connection.
      </p>
      <table>
        <thead>
          <tr>
            <th>State</th>
            <th>Who</th>
            <th>Do</th>
            <th>How the agent sees it</th>
          </tr>
        </thead>
        <tbody>
          {journey(PRODUCT_ORIGIN).map((step) => (
            <tr key={step.state}>
              <td>{step.state}</td>
              <td>{step.who}</td>
              <td>{coded(step.action)}</td>
              <td>{step.signal}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2 id="what-an-agent-can-do">What an agent can do</h2>
      <ul>
        <li>
          Say whether a job can start right now and, if not, what is in the way:
          no machine online, an empty balance, a spent budget.
        </li>
        <li>
          Write the workflow change, with a fallback runner for when no machine
          is free.
        </li>
        <li>
          Read machines, jobs, signed receipts, usage and what you have saved
          against GitHub-hosted runners.
        </li>
        <li>Compare prices for your volume before you move anything.</li>
        <li>
          With your approval as an owner: change hive settings, set a monthly
          budget and remove machines.
        </li>
      </ul>
      <p>
        An agent cannot add funds or a card. Payments are made by a person in
        the browser.
      </p>

      <h2 id="connect">Connect</h2>
      <h3 id="claude-code">Claude Code</h3>
      <CodeBlock title="Terminal" wrap>
        {clients.claudeCode}
      </CodeBlock>
      <p>
        Then run <Code>/mcp</Code> in a session, or{" "}
        <Code>{clients.claudeCodeLogin}</Code>, to sign in.{" "}
        <Code>--scope user</Code> makes it available in every project.
      </p>

      <h3 id="claude-desktop">Claude Desktop and claude.ai</h3>
      <p>
        Open Settings, then Connectors, then Add custom connector. Paste the URL
        above and choose Register automatically.
      </p>

      <h3 id="cursor">Cursor</h3>
      <CodeBlock title="mcp.json">{clients.cursor}</CodeBlock>

      <h3 id="codex">Codex CLI</h3>
      <CodeBlock title="Terminal" wrap>
        {`${clients.codex}\n${clients.codexLogin}`}
      </CodeBlock>

      <h3 id="other-clients">Other clients</h3>
      <p>
        Add an HTTP (Streamable HTTP) MCP server at the URL above. The client
        needs OAuth with dynamic client registration and PKCE, which is what the
        MCP specification asks for.
      </p>

      <h2 id="signing-in">Signing in</h2>
      <ol>
        <li>Your browser opens and you sign in with GitHub.</li>
        <li>
          You pick one hive. The connection can reach that hive and no other.
        </li>
        <li>
          You see the permissions the client asked for, and allow or deny them.
        </li>
      </ol>
      <p>
        You need to be in a hive first. If your organization has not installed
        HiveWorker, start with the{" "}
        <Link href="/docs/quickstart">Quickstart</Link>.
      </p>

      <h2 id="tools">Tools</h2>
      <p>
        An agent should call <Code>get_hive</Code> first. It answers most
        questions on its own and lists what to do next.
      </p>
      <table>
        <thead>
          <tr>
            <th>Tool</th>
            <th>What it does</th>
            <th>Permission</th>
            <th>Who</th>
          </tr>
        </thead>
        <tbody>
          {HIVE_TOOLS.map((tool) => (
            <tr key={tool.name}>
              <td>
                <Code>{tool.name}</Code>
              </td>
              <td>{tool.description}</td>
              <td>
                <Code>{tool.scope}</Code>
              </td>
              <td>{tool.role}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2 id="permissions">Permissions</h2>
      <ul>
        <li>
          <Code>hive:read</Code> reads the hive you picked: machines, jobs,
          balance, settings and receipts. Any member can grant it.
        </li>
        <li>
          <Code>hive:write</Code> changes settings, the budget, automatic top-up
          and machines. Only an owner or admin can grant it, and a client has to
          ask for it.
        </li>
      </ul>
      <p>
        Every request is checked against your membership as it is now. If you
        leave the organization on GitHub, the connection stops working.
      </p>

      <h2 id="revoke">Revoke a connection</h2>
      <p>
        <Link href="/connections">Agents</Link> in the console lists every
        connection you have approved. Revoking one stops it at once, including
        tokens already issued.
      </p>

      <h2 id="llms-txt">llms.txt</h2>
      <p>
        <a href={`${PRODUCT_ORIGIN}/llms.txt`}>/llms.txt</a> is a plain-text
        summary of HiveWorker for a model to read: what it is, the price, the
        MCP endpoint and the one-line workflow change (
        <Code>runs-on: {RUNNER_SIZES[0].label}</Code>
        ).
      </p>
    </>
  )
}
