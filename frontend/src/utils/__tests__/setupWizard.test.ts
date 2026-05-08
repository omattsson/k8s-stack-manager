import { describe, it, expect, vi, beforeEach } from 'vitest';
import { isSetupWizardDismissed, dismissSetupWizard, resetSetupWizard } from '../setupWizard';

describe('setupWizard', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  describe('isSetupWizardDismissed', () => {
    it('returns false when nothing stored', () => {
      expect(isSetupWizardDismissed()).toBe(false);
    });

    it('returns false for non-true values', () => {
      localStorage.setItem('setupWizardDismissed', 'false');
      expect(isSetupWizardDismissed()).toBe(false);
    });

    it('returns true after dismiss', () => {
      dismissSetupWizard();
      expect(isSetupWizardDismissed()).toBe(true);
    });

    it('handles localStorage errors gracefully', () => {
      const spy = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
        throw new Error('access denied');
      });
      expect(isSetupWizardDismissed()).toBe(false);
      spy.mockRestore();
    });
  });

  describe('dismissSetupWizard', () => {
    it('stores true in localStorage', () => {
      dismissSetupWizard();
      expect(localStorage.getItem('setupWizardDismissed')).toBe('true');
    });

    it('handles localStorage errors gracefully', () => {
      const spy = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
        throw new Error('quota exceeded');
      });
      expect(() => dismissSetupWizard()).not.toThrow();
      spy.mockRestore();
    });
  });

  describe('resetSetupWizard', () => {
    it('clears the dismissed state', () => {
      dismissSetupWizard();
      expect(isSetupWizardDismissed()).toBe(true);
      resetSetupWizard();
      expect(isSetupWizardDismissed()).toBe(false);
    });
  });
});
