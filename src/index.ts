/**
 * Octi Pulpo — Swarm Coordination Brain
 *
 * MCP server that provides shared memory, model routing,
 * agent coordination, and feedback loops for agent swarms.
 */

export { createServer } from './server.js';
export type { OctiConfig, SwarmMemory, AgentClaim } from './types.js';
