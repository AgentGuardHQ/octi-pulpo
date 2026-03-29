/** Octi Pulpo server configuration. */
export interface OctiConfig {
  /** Redis connection URL for hot state. */
  redisUrl: string;
  /** Vector DB endpoint for cold knowledge (Qdrant). */
  vectorDbUrl?: string;
  /** Port for the MCP server. */
  port: number;
  /** Namespace prefix for Redis keys. */
  namespace: string;
}

/** A stored memory entry in the swarm knowledge base. */
export interface SwarmMemory {
  id: string;
  /** The agent identity that stored this memory. */
  agentId: string;
  /** Topic tags for filtering and retrieval. */
  topics: string[];
  /** The actual content / learning / observation. */
  content: string;
  /** When this was stored. */
  storedAt: string;
  /** Optional vector embedding for semantic search. */
  embedding?: number[];
}

/** An agent's claim on a task (prevents duplicate work). */
export interface AgentClaim {
  /** Unique claim ID. */
  claimId: string;
  /** The agent identity holding the claim. */
  agentId: string;
  /** What the agent is working on. */
  task: string;
  /** When the claim was made. */
  claimedAt: string;
  /** TTL in seconds — claim expires if agent doesn't renew. */
  ttlSeconds: number;
}

/** A signal broadcast by an agent to the swarm. */
export interface SwarmSignal {
  /** The agent broadcasting. */
  agentId: string;
  /** Signal type. */
  type: 'completed' | 'blocked' | 'need-help' | 'directive' | 'heartbeat';
  /** Signal payload. */
  payload: string;
  /** When this was broadcast. */
  timestamp: string;
}

/** Model routing recommendation. */
export interface RouteRecommendation {
  /** Recommended model ID. */
  model: string;
  /** Why this model was chosen. */
  reason: string;
  /** Estimated cost for this task. */
  estimatedCost?: string;
  /** Alternative model if primary is unavailable. */
  fallback?: string;
}
