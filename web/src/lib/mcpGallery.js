// A short list of well-known MCP servers to start from. Picking one only pre-fills the "Add MCP server" form
// (nothing is installed or run until you save). Addresses and package names come from the projects' own docs
// and may change: if a connection fails, check the provider's current instructions.
export const MCP_GALLERY = [
  { name: 'filesystem', title: 'Filesystem', text: 'Read and write files in folders you choose.', transport: 'stdio', command: 'npx', args: ['-y', '@modelcontextprotocol/server-filesystem', '~/Documents'], needs: 'Node.js (npx) · edit the folder list', tag: 'local' },
  { name: 'memory', title: 'Knowledge graph memory', text: 'A small persistent memory of entities and relations.', transport: 'stdio', command: 'npx', args: ['-y', '@modelcontextprotocol/server-memory'], needs: 'Node.js (npx)', tag: 'local' },
  { name: 'thinking', title: 'Sequential thinking', text: 'Structured step-by-step reasoning scratchpad.', transport: 'stdio', command: 'npx', args: ['-y', '@modelcontextprotocol/server-sequential-thinking'], needs: 'Node.js (npx)', tag: 'local' },
  { name: 'fetch', title: 'Fetch', text: 'Fetch a URL and convert it to Markdown.', transport: 'stdio', command: 'uvx', args: ['mcp-server-fetch'], needs: 'uv (brew install uv)', tag: 'local' },
  { name: 'git', title: 'Git', text: 'Read, search and inspect a Git repository.', transport: 'stdio', command: 'uvx', args: ['mcp-server-git', '--repository', '~/Develpoment'], needs: 'uv (brew install uv) · edit the repository', tag: 'local' },
  { name: 'time', title: 'Time & time zones', text: 'Current time and time-zone conversions.', transport: 'stdio', command: 'uvx', args: ['mcp-server-time'], needs: 'uv (brew install uv)', tag: 'local' },
  { name: 'notion', title: 'Notion', text: 'Search and edit your Notion workspace.', transport: 'http', url: 'https://mcp.notion.com/mcp', needs: 'sign in with Notion (OAuth)', tag: 'sign-in' },
  { name: 'linear', title: 'Linear', text: 'Issues, projects and comments in Linear.', transport: 'http', url: 'https://mcp.linear.app/mcp', needs: 'sign in with Linear (OAuth)', tag: 'sign-in' },
  { name: 'sentry', title: 'Sentry', text: 'Errors and performance data from Sentry.', transport: 'http', url: 'https://mcp.sentry.dev/mcp', needs: 'sign in with Sentry (OAuth)', tag: 'sign-in' },
  { name: 'github', title: 'GitHub', text: 'Repositories, issues and pull requests.', transport: 'http', url: 'https://api.githubcopilot.com/mcp/', headersText: 'Authorization: Bearer <your GitHub token>', needs: 'a GitHub personal access token in the header', tag: 'token' },
];
