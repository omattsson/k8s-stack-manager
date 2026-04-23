import { describe, it, expect, vi, afterEach } from 'vitest';
import { timeAgo, notificationIcon, entityRoute } from '../notificationHelpers';

describe('notificationHelpers', () => {
  describe('timeAgo', () => {
    afterEach(() => {
      vi.useRealTimers();
    });

    it('returns "just now" for less than 60 seconds ago', () => {
      vi.useFakeTimers();
      vi.setSystemTime(new Date('2026-01-15T12:00:30Z'));
      expect(timeAgo('2026-01-15T12:00:00Z')).toBe('just now');
    });

    it('returns minutes ago', () => {
      vi.useFakeTimers();
      vi.setSystemTime(new Date('2026-01-15T12:05:00Z'));
      expect(timeAgo('2026-01-15T12:00:00Z')).toBe('5m ago');
    });

    it('returns hours ago', () => {
      vi.useFakeTimers();
      vi.setSystemTime(new Date('2026-01-15T15:00:00Z'));
      expect(timeAgo('2026-01-15T12:00:00Z')).toBe('3h ago');
    });

    it('returns days ago', () => {
      vi.useFakeTimers();
      vi.setSystemTime(new Date('2026-01-25T12:00:00Z'));
      expect(timeAgo('2026-01-15T12:00:00Z')).toBe('10d ago');
    });

    it('returns formatted date for > 30 days', () => {
      vi.useFakeTimers();
      vi.setSystemTime(new Date('2026-06-15T12:00:00Z'));
      const result = timeAgo('2026-01-15T12:00:00Z');
      expect(result).not.toBe('');
      expect(result).not.toContain('ago');
    });

    it('returns dateStr for invalid date', () => {
      expect(timeAgo('not-a-date')).toBe('not-a-date');
    });

    it('returns "unknown" for empty string', () => {
      expect(timeAgo('')).toBe('unknown');
    });
  });

  describe('notificationIcon', () => {
    it('returns success icon for success type', () => {
      const icon = notificationIcon('deployment.success');
      expect(icon).toBeTruthy();
      expect(icon.props.color).toBe('success');
    });

    it('returns error icon for error type', () => {
      const icon = notificationIcon('deployment.error');
      expect(icon).toBeTruthy();
      expect(icon.props.color).toBe('error');
    });

    it('returns warning icon for stopped type', () => {
      const icon = notificationIcon('deployment.stopped');
      expect(icon).toBeTruthy();
      expect(icon.props.color).toBe('warning');
    });

    it('returns action icon for deleted type', () => {
      const icon = notificationIcon('instance.deleted');
      expect(icon).toBeTruthy();
      expect(icon.props.color).toBe('action');
    });

    it('returns success icon for completed type', () => {
      const icon = notificationIcon('clean.completed');
      expect(icon).toBeTruthy();
      expect(icon.props.color).toBe('success');
    });

    it('returns success icon for rollback.completed', () => {
      const icon = notificationIcon('rollback.completed');
      expect(icon).toBeTruthy();
      expect(icon.props.color).toBe('success');
    });

    it('returns error icon for clean.error', () => {
      const icon = notificationIcon('clean.error');
      expect(icon).toBeTruthy();
      expect(icon.props.color).toBe('error');
    });

    it('returns error icon for rollback.error', () => {
      const icon = notificationIcon('rollback.error');
      expect(icon).toBeTruthy();
      expect(icon.props.color).toBe('error');
    });

    it('returns success icon for instance.created', () => {
      const icon = notificationIcon('instance.created');
      expect(icon).toBeTruthy();
      expect(icon.props.color).toBe('success');
    });

    it('returns info icon for unknown type', () => {
      const icon = notificationIcon('something.else');
      expect(icon).toBeTruthy();
      expect(icon.props.color).toBe('info');
    });
  });

  describe('entityRoute', () => {
    it('returns /stack-instances for stack_instance', () => {
      expect(entityRoute('stack_instance')).toBe('/stack-instances');
    });

    it('returns /stack-definitions for stack_definition', () => {
      expect(entityRoute('stack_definition')).toBe('/stack-definitions');
    });

    it('returns /templates for stack_template', () => {
      expect(entityRoute('stack_template')).toBe('/templates');
    });

    it('returns empty string for unknown type', () => {
      expect(entityRoute('unknown')).toBe('');
    });

    it('returns empty string for undefined', () => {
      expect(entityRoute(undefined)).toBe('');
    });
  });
});
