import { describe, it, expect } from 'vitest';
import {
  ChannelManager,
  createChannelManager,
  validateOwnerId,
  taskChannel,
  userTaskPattern,
  registryAllChannel,
  registrySkillChannel,
  registryVisibilityChannel,
  registryLogChannel,
  normalizeSkillSlug,
} from '../src/runtime/channel-manager.js';

describe('ChannelManager', () => {
  it('throws error when agentName is not specified', () => {
    // ChannelManager now requires agentName - no default fallback
    expect(() => new ChannelManager()).toThrow('agentName is required for ChannelManager');
    expect(() => new ChannelManager({})).toThrow('agentName is required for ChannelManager');
  });

  it('uses specified agent name', () => {
    const cm = new ChannelManager({ agentName: 'acme-echo' });
    expect(cm.controlChannel('agent-id-1')).toBe('agent.agent-id-1.control');
    expect(cm.obsChannel()).toBe('obs.acme-echo.log');
    expect(cm.getAgentName()).toBe('acme-echo');
  });

  it('allows override in method calls', () => {
    const cm = new ChannelManager({ agentName: 'acme-echo' });
    expect(cm.controlChannel('agent-id-kyc')).toBe('agent.agent-id-kyc.control');
    expect(cm.obsChannel('fraud')).toBe('obs.fraud.log');
  });

  it('generates correct task channel with orgId', () => {
    const cm = new ChannelManager({ agentName: 'acme-echo' });
    expect(cm.taskChannel('task-123', 'alice')).toBe('u.alice.task-123');
    expect(cm.taskChannel('task-456', 'bob')).toBe('u.bob.task-456');
  });

  it('throws if orgId is missing for taskChannel', () => {
    const cm = new ChannelManager({ agentName: 'acme-echo' });
    expect(() => cm.taskChannel('task-123', '')).toThrow('orgId required');
    expect(() => cm.taskChannel('task-123', undefined as unknown as string)).toThrow(
      'orgId required',
    );
  });

  it('throws if taskId is missing for taskChannel', () => {
    const cm = new ChannelManager({ agentName: 'acme-echo' });
    expect(() => cm.taskChannel('', 'alice')).toThrow('taskId required');
    expect(() => cm.taskChannel(undefined as unknown as string, 'alice')).toThrow(
      'taskId required',
    );
  });

  it('generates correct user task pattern for PAM grants', () => {
    const cm = new ChannelManager({ agentName: 'acme-echo' });
    expect(cm.userTaskPattern('alice')).toBe('u.alice.*');
    expect(cm.userTaskPattern('bob')).toBe('u.bob.*');
  });

  it('throws if orgId is missing for userTaskPattern', () => {
    const cm = new ChannelManager({ agentName: 'acme-echo' });
    expect(() => cm.userTaskPattern('')).toThrow('orgId required');
    expect(() => cm.userTaskPattern(undefined as unknown as string)).toThrow('orgId required');
  });

  it('generates correct wildcard patterns', () => {
    const cm = new ChannelManager({ agentName: 'acme-echo' });
    expect(cm.agentWildcard()).toBe('agent.acme-echo.*');
    expect(cm.agentWildcard('kyc')).toBe('agent.kyc.*');
  });

  it('factory function creates manager with agent name', () => {
    const cm = createChannelManager('kyc');
    expect(cm.controlChannel('kyc-agent-id')).toBe('agent.kyc-agent-id.control');
    expect(cm.obsChannel()).toBe('obs.kyc.log');
  });

  it('factory function requires agentName parameter', () => {
    // createChannelManager now requires an agentName parameter
    // TypeScript enforces this, but runtime also validates
    expect(() => createChannelManager(undefined as unknown as string)).toThrow();
    expect(() => createChannelManager('' as string)).toThrow();
  });

  it('handles various agent name strings', () => {
    const types = ['acme-echo', 'kyc', 'fraud-detection', 'payment_processor', 'agent123'];
    for (const type of types) {
      const cm = createChannelManager(type);
      expect(cm.controlChannel(`${type}-id`)).toBe(`agent.${type}-id.control`);
      expect(cm.obsChannel()).toBe(`obs.${type}.log`);
    }
  });

});

describe('Standalone taskChannel and userTaskPattern', () => {
  it('taskChannel formats as u.{orgId}.{taskId}', () => {
    expect(taskChannel('task-1', 'acme-corp')).toBe('u.acme-corp.task-1');
  });

  it('taskChannel throws when orgId is empty', () => {
    expect(() => taskChannel('task-1', '')).toThrow('orgId required');
  });

  it('taskChannel throws when taskId is empty', () => {
    expect(() => taskChannel('', 'acme-corp')).toThrow('taskId required');
  });

  it('userTaskPattern formats as u.{orgId}.*', () => {
    expect(userTaskPattern('acme-corp')).toBe('u.acme-corp.*');
  });

  it('userTaskPattern throws when orgId is empty', () => {
    expect(() => userTaskPattern('')).toThrow('orgId required');
  });
});

describe('Registry Channel Helpers', () => {
  it('registryAllChannel returns registry.all', () => {
    expect(registryAllChannel()).toBe('registry.all');
  });

  it('registrySkillChannel returns registry.skill.{skill}', () => {
    expect(registrySkillChannel('image_generation')).toBe('registry.skill.image_generation');
    expect(registrySkillChannel('text.embeddings')).toBe('registry.skill.text.embeddings');
  });

  it('registryVisibilityChannel returns registry.public or registry.private', () => {
    expect(registryVisibilityChannel(true)).toBe('registry.public');
    expect(registryVisibilityChannel(false)).toBe('registry.private');
  });

  it('registryLogChannel returns registry.log', () => {
    expect(registryLogChannel()).toBe('registry.log');
  });
});

describe('normalizeSkillSlug', () => {
  it('converts to lowercase', () => {
    expect(normalizeSkillSlug('IMAGE_GENERATION')).toBe('image_generation');
    expect(normalizeSkillSlug('TextEmbeddings')).toBe('textembeddings');
  });

  it('replaces dashes with underscores', () => {
    expect(normalizeSkillSlug('image-generation')).toBe('image_generation');
    expect(normalizeSkillSlug('text-to-speech')).toBe('text_to_speech');
  });

  it('preserves dots', () => {
    expect(normalizeSkillSlug('text.embeddings')).toBe('text.embeddings');
    expect(normalizeSkillSlug('ai.vision.ocr')).toBe('ai.vision.ocr');
  });

  it('replaces spaces with underscores', () => {
    expect(normalizeSkillSlug('Image Generation')).toBe('image_generation');
  });

  it('collapses multiple underscores', () => {
    expect(normalizeSkillSlug('image--generation')).toBe('image_generation');
    expect(normalizeSkillSlug('image___generation')).toBe('image_generation');
  });

  it('removes leading and trailing underscores', () => {
    expect(normalizeSkillSlug('_image_generation_')).toBe('image_generation');
    expect(normalizeSkillSlug('__test__')).toBe('test');
  });

  it('handles complex cases', () => {
    expect(normalizeSkillSlug('AI-Powered Image Generation!')).toBe('ai_powered_image_generation');
  });
});

describe('validateOwnerId', () => {
  it('returns true for valid owner IDs', () => {
    expect(validateOwnerId('alice')).toBe(true);
    expect(validateOwnerId('bob123')).toBe(true);
    expect(validateOwnerId('user-with-dashes')).toBe(true);
    expect(validateOwnerId('user_with_underscores')).toBe(true);
  });

  it('returns false for reserved prefixes', () => {
    expect(validateOwnerId('agent')).toBe(false);
    expect(validateOwnerId('Agent')).toBe(false);
    expect(validateOwnerId('AGENT')).toBe(false);
    expect(validateOwnerId('obs')).toBe(false);
    expect(validateOwnerId('sys')).toBe(false);
    expect(validateOwnerId('u')).toBe(false);
    expect(validateOwnerId('anonymous')).toBe(false);
    expect(validateOwnerId('system')).toBe(false);
  });

  it('returns false for empty or invalid inputs', () => {
    expect(validateOwnerId('')).toBe(false);
    expect(validateOwnerId(null as unknown as string)).toBe(false);
    expect(validateOwnerId(undefined as unknown as string)).toBe(false);
  });
});

describe('Skill Channel Extraction', () => {
  /**
   * These tests verify the skill extraction logic that matches the
   * dashboard's extractSkillFromChannel() function. This ensures
   * skills can be derived from registry.skill.* membership channels.
   */

  /**
   * Extract skill slug from a registry skill channel name.
   * This mirrors the dashboard's extractSkillFromChannel() function.
   */
  function extractSkillFromChannel(channel: string): string | null {
    const prefix = 'registry.skill.';
    if (channel.startsWith(prefix)) {
      return channel.slice(prefix.length);
    }
    return null;
  }

  it('extracts skill from registry.skill.* channels', () => {
    expect(extractSkillFromChannel('registry.skill.echo')).toBe('echo');
    expect(extractSkillFromChannel('registry.skill.image_generation')).toBe('image_generation');
    expect(extractSkillFromChannel('registry.skill.text.embeddings')).toBe('text.embeddings');
    expect(extractSkillFromChannel('registry.skill.ai.vision.ocr')).toBe('ai.vision.ocr');
  });

  it('returns null for non-skill channels', () => {
    expect(extractSkillFromChannel('registry.all')).toBeNull();
    expect(extractSkillFromChannel('registry.public')).toBeNull();
    expect(extractSkillFromChannel('registry.private')).toBeNull();
    expect(extractSkillFromChannel('registry.log')).toBeNull();
    expect(extractSkillFromChannel('other.channel')).toBeNull();
    expect(extractSkillFromChannel('')).toBeNull();
  });

  it('round-trips skill through registrySkillChannel and extraction', () => {
    // Test that we can normalize, create channel, and extract back
    const testCases = [
      { input: 'echo', normalized: 'echo' },
      { input: 'image-generation', normalized: 'image_generation' },
      { input: 'text.embeddings', normalized: 'text.embeddings' },
      { input: 'Image Generation', normalized: 'image_generation' },
      { input: 'AI-Powered OCR', normalized: 'ai_powered_ocr' },
    ];

    for (const { input, normalized } of testCases) {
      const normalizedSlug = normalizeSkillSlug(input);
      expect(normalizedSlug).toBe(normalized);

      const channel = registrySkillChannel(input);
      expect(channel).toBe(`registry.skill.${normalized}`);

      const extracted = extractSkillFromChannel(channel);
      expect(extracted).toBe(normalized);
    }
  });

  it('handles edge cases in extraction', () => {
    // Channel exactly matching prefix (no skill)
    expect(extractSkillFromChannel('registry.skill.')).toBe('');
    // Channels with partial prefix match
    expect(extractSkillFromChannel('registry.skills.echo')).toBeNull();
    expect(extractSkillFromChannel('registry.skill_set.echo')).toBeNull();
  });
});

describe('Skill Derivation from Memberships', () => {
  /**
   * These tests verify the pattern used by the dashboard to derive
   * skills from channel memberships rather than from card data.
   */

  function extractSkillFromChannel(channel: string): string | null {
    const prefix = 'registry.skill.';
    if (channel.startsWith(prefix)) {
      return channel.slice(prefix.length);
    }
    return null;
  }

  it('filters skills from membership list', () => {
    // Simulate a getMemberships response
    const memberships = [
      { channel: { id: 'registry.all' } },
      { channel: { id: 'registry.public' } },
      { channel: { id: 'registry.skill.echo' } },
      { channel: { id: 'registry.skill.text_generation' } },
      { channel: { id: 'registry.skill.ai.vision' } },
    ];

    const skills = memberships
      .map((m) => extractSkillFromChannel(m.channel.id))
      .filter((skill): skill is string => skill !== null);

    expect(skills).toEqual(['echo', 'text_generation', 'ai.vision']);
    expect(skills).not.toContain('all');
    expect(skills).not.toContain('public');
  });

  it('handles empty membership list', () => {
    const memberships: Array<{ channel: { id: string } }> = [];

    const skills = memberships
      .map((m) => extractSkillFromChannel(m.channel.id))
      .filter((skill): skill is string => skill !== null);

    expect(skills).toEqual([]);
  });

  it('handles memberships with only non-skill channels', () => {
    const memberships = [
      { channel: { id: 'registry.all' } },
      { channel: { id: 'registry.private' } },
    ];

    const skills = memberships
      .map((m) => extractSkillFromChannel(m.channel.id))
      .filter((skill): skill is string => skill !== null);

    expect(skills).toEqual([]);
  });
});
