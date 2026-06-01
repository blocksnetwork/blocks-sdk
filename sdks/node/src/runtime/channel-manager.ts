/**
 * ChannelManager - Centralized channel naming utility for agent-name sharding.
 *
 * Channel topology (org-scoped with 3-level constraint):
 * - u.{orgId}.{taskId}             - Org-scoped task events (progress, artifacts, terminal, logs)
 * - agent.{agentId}.control         - Control plane per agent ID
 * - obs.{agentName}.log            - Agent-level observability
 *
 * Registry channels (membership indexes):
 * - registry.all                   - All agents (membership index)
 * - registry.public                - Public agents (membership index)
 * - registry.private               - Private agents (membership index)
 * - registry.tag.{tag}             - Agents with specific tag (membership index)
 * - registry.log                   - Audit log channel (pub/sub with history)
 *
 * Channel metadata (not pub/sub):
 * - uuid:{agentName}               - Agent card stored as User Object
 *
 * All channels respect PubNub's 3-level maximum depth constraint.
 * Subscribe filters are used for agent-name scoping on task channels.
 */

// ============================================================================
// Registry Channel Helpers (membership indexes)
// ============================================================================

/**
 * Registry "all" channel - membership index for all agents.
 * Format: registry.all
 */
export const registryAllChannel = (): string => 'registry.all';

/**
 * Registry tag channel - membership index for agents with a tag.
 * Format: registry.tag.{tag}
 *
 * Tag slugs are normalized: lowercase, alphanumeric + `.` or `_`.
 */
export const registryTagChannel = (tag: string): string => {
  const slug = normalizeTagSlug(tag);
  return `registry.tag.${slug}`;
};

/**
 * Registry visibility channel - membership index for public/private agents.
 * Format: registry.public or registry.private
 */
export const registryVisibilityChannel = (isPublic: boolean): string => {
  return isPublic ? 'registry.public' : 'registry.private';
};

/**
 * Registry audit log channel - pub/sub channel for registry change events.
 * Format: registry.log
 */
export const registryLogChannel = (): string => 'registry.log';

/**
 * Normalize a tag string to a stable slug.
 * - Lowercase
 * - Replace non-alphanumeric characters (except `.` and `_`) with `_`
 * - Collapse multiple underscores
 *
 * Examples:
 * - "image-generation" -> "image_generation"
 * - "text.embeddings" -> "text.embeddings"
 * - "Image Generation" -> "image_generation"
 */
export const normalizeTagSlug = (tag: string): string => {
  return tag
    .toLowerCase()
    .replace(/[^a-z0-9._]/g, '_')
    .replace(/_+/g, '_')
    .replace(/^_|_$/g, '');
};

// Reserved prefixes that cannot be used as owner IDs
const RESERVED_PREFIXES = ['agent', 'obs', 'sys', 'u', 'anonymous', 'system', 'stream'];

/**
 * Validates that an owner ID is not a reserved prefix.
 */
export function validateOwnerId(ownerId: string): boolean {
  if (!ownerId || typeof ownerId !== 'string') {
    return false;
  }
  return !RESERVED_PREFIXES.includes(ownerId.toLowerCase());
}

export interface ChannelManagerOptions {
  agentName?: string;
}

export class ChannelManager {
  private readonly agentName: string;

  constructor(opts: ChannelManagerOptions = {}) {
    if (!opts.agentName) {
      throw new Error('agentName is required for ChannelManager');
    }
    this.agentName = opts.agentName;
  }

  getAgentName(): string {
    return this.agentName;
  }

  /**
   * Org-scoped task channel.
   * All task events (progress, artifacts, terminal, logs) publish here.
   * Format: u.{orgId}.{taskId} (3 levels)
   */
  taskChannel(taskId: string, orgId: string): string {
    if (!orgId) {
      throw new Error('orgId required for task channel');
    }
    if (!taskId) {
      throw new Error('taskId required for task channel');
    }
    return `u.${orgId}.${taskId}`;
  }

  /**
   * Control channel for agent.
   * Format: agent.{agentId}.control (3 levels)
   */
  controlChannel(agentId: string): string {
    return `agent.${agentId}.control`;
  }

  /**
   * Agent-level observability channel.
   * Format: obs.{agentName}.log (3 levels)
   */
  obsChannel(agentName?: string): string {
    return `obs.${agentName ?? this.agentName}.log`;
  }

  /**
   * Wildcard pattern for org's task channels (for PAM grants).
   * Format: u.{orgId}.* (grants access to all tasks for this org)
   */
  userTaskPattern(orgId: string): string {
    if (!orgId) {
      throw new Error('orgId required for user task pattern');
    }
    return `u.${orgId}.*`;
  }

  /**
   * Wildcard pattern for agent channels.
   * Format: agent.{agentName}.* (for PAM grants)
   */
  agentWildcard(agentName?: string): string {
    return `agent.${agentName ?? this.agentName}.*`;
  }

  /**
   * Stream data channel for an agent name and stream ID.
   * Format: stream.{agentName}.{streamId} (3 levels).
   *
   * `streamId` is always SDK-derived, never caller-supplied:
   * - Dedicated-affinity streams: `{taskId}-{counter}` (per-task,
   *   counter increments per createStream() call within the task).
   * - Shared-affinity streams: the card-declared key (constant
   *   across tasks by design).
   */
  streamChannel(streamId: string, agentName?: string): string {
    if (!streamId) {
      throw new Error('streamId required for stream channel');
    }
    return `stream.${agentName ?? this.agentName}.${streamId}`;
  }

  /**
   * Wildcard pattern for all stream channels of an agent.
   * Format: stream.{agentName}.* (for PAM grants)
   */
  streamWildcard(agentName?: string): string {
    return `stream.${agentName ?? this.agentName}.*`;
  }
}

// Note: No default instance - callers must specify agentName explicitly

// Factory function for creating managers with specific agent names
export const createChannelManager = (agentName: string): ChannelManager => {
  return new ChannelManager({ agentName });
};

// ============================================================================
// Standalone helper functions for channels that don't depend on agentName
// ============================================================================

/**
 * Org-scoped task channel (standalone function).
 * Format: u.{orgId}.{taskId}
 */
export const taskChannel = (taskId: string, orgId: string): string => {
  if (!orgId) {
    throw new Error('orgId required for task channel');
  }
  if (!taskId) {
    throw new Error('taskId required for task channel');
  }
  return `u.${orgId}.${taskId}`;
};

/**
 * Wildcard pattern for org's task channels (standalone function).
 * Format: u.{orgId}.*
 */
export const userTaskPattern = (orgId: string): string => {
  if (!orgId) {
    throw new Error('orgId required for user task pattern');
  }
  return `u.${orgId}.*`;
};

/**
 * Stream data channel (standalone function).
 * Format: stream.{agentName}.{streamId}
 */
export const streamChannel = (agentName: string, streamId: string): string => {
  if (!agentName) {
    throw new Error('agentName required for stream channel');
  }
  if (!streamId) {
    throw new Error('streamId required for stream channel');
  }
  return `stream.${agentName}.${streamId}`;
};
