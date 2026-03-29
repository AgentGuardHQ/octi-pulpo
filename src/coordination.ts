import Redis from 'ioredis';
import type { OctiConfig, AgentClaim, SwarmSignal } from './types.js';

/** Agent coordination engine — claims, signals, status. */
export class CoordinationEngine {
  private redis: Redis;
  private ns: string;

  constructor(config: OctiConfig) {
    this.redis = new Redis(config.redisUrl);
    this.ns = config.namespace;
  }

  /** Claim a task. Returns the claim. Existing claims by other agents cause rejection. */
  async claim(agentId: string, task: string, ttlSeconds: number): Promise<AgentClaim> {
    const claimId = `${agentId}:${Date.now()}`;
    const claim: AgentClaim = {
      claimId,
      agentId,
      task,
      claimedAt: new Date().toISOString(),
      ttlSeconds,
    };
    const key = `${this.ns}:claim:${agentId}`;
    await this.redis.set(key, JSON.stringify(claim), 'EX', ttlSeconds);
    // Also add to the active claims set for listing
    await this.redis.zadd(`${this.ns}:active-claims`, Date.now(), JSON.stringify(claim));
    return claim;
  }

  /** Get all active claims across the swarm. */
  async activeClaims(): Promise<AgentClaim[]> {
    const key = `${this.ns}:active-claims`;
    const raw = await this.redis.zrevrange(key, 0, 50);
    const claims: AgentClaim[] = [];
    for (const r of raw) {
      const claim = JSON.parse(r) as AgentClaim;
      // Check if the claim's TTL key still exists
      const exists = await this.redis.exists(`${this.ns}:claim:${claim.agentId}`);
      if (exists) claims.push(claim);
    }
    return claims;
  }

  /** Broadcast a signal to the swarm. */
  async signal(agentId: string, type: SwarmSignal['type'], payload: string): Promise<void> {
    const sig: SwarmSignal = {
      agentId,
      type,
      payload,
      timestamp: new Date().toISOString(),
    };
    const key = `${this.ns}:signals`;
    await this.redis.zadd(key, Date.now(), JSON.stringify(sig));
    // Trim to last 500 signals
    await this.redis.zremrangebyrank(key, 0, -501);
    // Publish for real-time listeners
    await this.redis.publish(`${this.ns}:signal-stream`, JSON.stringify(sig));
  }

  /** Get recent signals. */
  async recentSignals(limit = 20): Promise<SwarmSignal[]> {
    const key = `${this.ns}:signals`;
    const raw = await this.redis.zrevrange(key, 0, limit - 1);
    return raw.map((r) => JSON.parse(r) as SwarmSignal);
  }

  async close(): Promise<void> {
    await this.redis.quit();
  }
}
