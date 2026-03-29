import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import {
  CallToolRequestSchema,
  ListToolsRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';
import type { OctiConfig } from './types.js';
import { MemoryStore } from './memory.js';
import { CoordinationEngine } from './coordination.js';

const TOOLS = [
  {
    name: 'memory_store',
    description: 'Store a learning, observation, or decision in the swarm knowledge base. Tagged with your agent identity and topics for retrieval by other agents.',
    inputSchema: {
      type: 'object' as const,
      properties: {
        content: { type: 'string', description: 'What you learned / observed / decided' },
        topics: { type: 'array', items: { type: 'string' }, description: 'Topic tags (e.g. ["bootstrap", "worktree", "pnpm"])' },
      },
      required: ['content', 'topics'],
    },
  },
  {
    name: 'memory_recall',
    description: 'Search the swarm knowledge base. Returns relevant learnings from all agents, ranked by relevance.',
    inputSchema: {
      type: 'object' as const,
      properties: {
        query: { type: 'string', description: 'What are you looking for?' },
        limit: { type: 'number', description: 'Max results (default 5)' },
      },
      required: ['query'],
    },
  },
  {
    name: 'memory_status',
    description: 'See what other agents in the swarm are currently working on.',
    inputSchema: { type: 'object' as const, properties: {} },
  },
  {
    name: 'coord_claim',
    description: 'Claim a task so no other agent duplicates your work. Claims auto-expire if not renewed.',
    inputSchema: {
      type: 'object' as const,
      properties: {
        task: { type: 'string', description: 'What you are working on' },
        ttlSeconds: { type: 'number', description: 'How long to hold the claim (default 900)' },
      },
      required: ['task'],
    },
  },
  {
    name: 'coord_signal',
    description: 'Broadcast a signal to the swarm — completion, blocker, or need-help.',
    inputSchema: {
      type: 'object' as const,
      properties: {
        type: { type: 'string', enum: ['completed', 'blocked', 'need-help', 'directive'], description: 'Signal type' },
        payload: { type: 'string', description: 'Details' },
      },
      required: ['type', 'payload'],
    },
  },
  {
    name: 'route_recommend',
    description: 'Get the recommended model for a task type based on cost, capability, and swarm history.',
    inputSchema: {
      type: 'object' as const,
      properties: {
        taskDescription: { type: 'string', description: 'Describe the task' },
        budget: { type: 'string', enum: ['low', 'medium', 'high'], description: 'Budget tier' },
      },
      required: ['taskDescription'],
    },
  },
];

export function createServer(config: OctiConfig): Server {
  const memory = new MemoryStore(config);
  const coordination = new CoordinationEngine(config);
  const server = new Server(
    { name: 'octi-pulpo', version: '0.1.0' },
    { capabilities: { tools: {} } }
  );

  server.setRequestHandler(ListToolsRequestSchema, async () => ({
    tools: TOOLS,
  }));

  server.setRequestHandler(CallToolRequestSchema, async (request) => {
    const { name, arguments: args } = request.params;
    const agentId = process.env.AGENTGUARD_AGENT_NAME ?? 'unknown';

    switch (name) {
      case 'memory_store': {
        const { content, topics } = args as { content: string; topics: string[] };
        const id = await memory.store(agentId, content, topics);
        return { content: [{ type: 'text', text: `Stored memory ${id} with topics: ${topics.join(', ')}` }] };
      }
      case 'memory_recall': {
        const { query, limit } = args as { query: string; limit?: number };
        const results = await memory.recall(query, limit ?? 5);
        const text = results.length === 0
          ? 'No relevant memories found.'
          : results.map((m, i) => `${i + 1}. [${m.agentId}] ${m.content} (topics: ${m.topics.join(', ')})`).join('\n');
        return { content: [{ type: 'text', text }] };
      }
      case 'memory_status': {
        const claims = await coordination.activeClaims();
        const text = claims.length === 0
          ? 'No agents have active claims right now.'
          : claims.map(c => `- ${c.agentId}: ${c.task} (claimed ${c.claimedAt})`).join('\n');
        return { content: [{ type: 'text', text }] };
      }
      case 'coord_claim': {
        const { task, ttlSeconds } = args as { task: string; ttlSeconds?: number };
        const claim = await coordination.claim(agentId, task, ttlSeconds ?? 900);
        return { content: [{ type: 'text', text: `Claimed: "${task}" (expires in ${claim.ttlSeconds}s)` }] };
      }
      case 'coord_signal': {
        const { type, payload } = args as { type: string; payload: string };
        await coordination.signal(agentId, type as 'completed' | 'blocked' | 'need-help' | 'directive', payload);
        return { content: [{ type: 'text', text: `Signal broadcast: ${type} — ${payload}` }] };
      }
      case 'route_recommend': {
        const { taskDescription, budget } = args as { taskDescription: string; budget?: string };
        // Placeholder — will be replaced with actual routing logic using swarm history
        const recommendation = budget === 'low'
          ? { model: 'copilot', reason: 'Low budget — tier C agent' }
          : { model: 'claude-opus-4', reason: 'Complex task — tier A agent' };
        return { content: [{ type: 'text', text: `Recommended: ${recommendation.model} — ${recommendation.reason}` }] };
      }
      default:
        return { content: [{ type: 'text', text: `Unknown tool: ${name}` }], isError: true };
    }
  });

  return server;
}

/** Start the MCP server on stdio. */
export async function startStdio(config: OctiConfig): Promise<void> {
  const server = createServer(config);
  const transport = new StdioServerTransport();
  await server.connect(transport);
}
