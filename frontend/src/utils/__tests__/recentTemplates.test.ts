import { describe, it, expect, vi, beforeEach } from 'vitest';
import { getRecentTemplates, trackRecentTemplate } from '../recentTemplates';

describe('recentTemplates', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  describe('getRecentTemplates', () => {
    it('returns empty array when nothing stored', () => {
      expect(getRecentTemplates()).toEqual([]);
    });

    it('returns empty array for invalid JSON', () => {
      localStorage.setItem('recentTemplates', 'not-json');
      expect(getRecentTemplates()).toEqual([]);
    });

    it('returns empty array for non-array JSON', () => {
      localStorage.setItem('recentTemplates', '{"key": "value"}');
      expect(getRecentTemplates()).toEqual([]);
    });

    it('filters out invalid entries', () => {
      localStorage.setItem('recentTemplates', JSON.stringify([
        { id: 't1', name: 'Valid', usedAt: '2026-01-01T00:00:00Z' },
        { id: 123, name: 'Bad ID' },
        null,
        'string',
      ]));
      const result = getRecentTemplates();
      expect(result).toHaveLength(1);
      expect(result[0].id).toBe('t1');
    });

    it('limits to 5 entries', () => {
      const items = Array.from({ length: 8 }, (_, i) => ({
        id: `t${i}`, name: `Template ${i}`, usedAt: '2026-01-01T00:00:00Z',
      }));
      localStorage.setItem('recentTemplates', JSON.stringify(items));
      expect(getRecentTemplates()).toHaveLength(5);
    });

    it('returns valid stored templates', () => {
      const items = [
        { id: 't1', name: 'Alpha', usedAt: '2026-01-01T00:00:00Z' },
        { id: 't2', name: 'Beta', usedAt: '2026-01-02T00:00:00Z' },
      ];
      localStorage.setItem('recentTemplates', JSON.stringify(items));
      const result = getRecentTemplates();
      expect(result).toHaveLength(2);
      expect(result[0].name).toBe('Alpha');
    });
  });

  describe('trackRecentTemplate', () => {
    it('adds a new template', () => {
      trackRecentTemplate({ id: 't1', name: 'First' });
      const result = getRecentTemplates();
      expect(result).toHaveLength(1);
      expect(result[0].id).toBe('t1');
      expect(result[0].usedAt).toBeDefined();
    });

    it('moves existing template to front', () => {
      trackRecentTemplate({ id: 't1', name: 'First' });
      trackRecentTemplate({ id: 't2', name: 'Second' });
      trackRecentTemplate({ id: 't1', name: 'First Updated' });
      const result = getRecentTemplates();
      expect(result).toHaveLength(2);
      expect(result[0].id).toBe('t1');
      expect(result[0].name).toBe('First Updated');
    });

    it('limits to 5 entries', () => {
      for (let i = 0; i < 7; i++) {
        trackRecentTemplate({ id: `t${i}`, name: `Template ${i}` });
      }
      expect(getRecentTemplates()).toHaveLength(5);
    });

    it('handles corrupt localStorage gracefully', () => {
      localStorage.setItem('recentTemplates', 'corrupt');
      // trackRecentTemplate catches JSON.parse error from corrupt data
      // and falls back to an empty array, then saves the new entry
      expect(() => trackRecentTemplate({ id: 't1', name: 'New' })).not.toThrow();
    });

    it('handles localStorage errors gracefully', () => {
      const spy = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
        throw new Error('quota exceeded');
      });
      expect(() => trackRecentTemplate({ id: 't1', name: 'Test' })).not.toThrow();
      spy.mockRestore();
    });
  });
});
