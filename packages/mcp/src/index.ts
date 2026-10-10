import { createMcpHandler, McpServer } from "@modelcontextprotocol/server"
export { McpServer }

/** A new server per request. Authentication belongs to the app's boundary. */
export function createMcpEndpoint(
  register: (server: McpServer) => void,
  identity = { name: "starter", version: "1.0.0" },
  /** `instructions` is what a client shows its model about this server. */
  options: { instructions?: string } = {}
) {
  const handler = createMcpHandler(() => {
    const server = new McpServer(identity, options)
    register(server)
    return server
  })
  return (request: Request) => handler.fetch(request)
}

export function toolResult(value: Record<string, unknown>) {
  return {
    content: [{ type: "text" as const, text: JSON.stringify(value) }],
    structuredContent: value,
  }
}
