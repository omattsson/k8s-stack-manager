import { describe, it, expect, vi, afterEach } from 'vitest';
import { downloadBlob } from '../download';

describe('downloadBlob', () => {
  const originalCreate = URL.createObjectURL;
  const originalRevoke = URL.revokeObjectURL;

  afterEach(() => {
    URL.createObjectURL = originalCreate;
    URL.revokeObjectURL = originalRevoke;
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('saves the Blob unchanged under the file name and revokes the URL', () => {
    vi.useFakeTimers();
    const createSpy = vi.fn().mockReturnValue('blob:test');
    const revokeSpy = vi.fn();
    URL.createObjectURL = createSpy;
    URL.revokeObjectURL = revokeSpy;
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      expect(this.download).toBe('demo-values.zip');
      expect(this.href).toBe('blob:test');
    });

    const blob = new Blob(['PK\u0003\u0004'], { type: 'application/zip' });
    downloadBlob(blob, 'demo-values.zip');

    expect(createSpy).toHaveBeenCalledWith(blob);
    expect(clickSpy).toHaveBeenCalledTimes(1);
    expect(document.querySelector('a[download]')).toBeNull();
    vi.advanceTimersByTime(999);
    expect(revokeSpy).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(revokeSpy).toHaveBeenCalledWith('blob:test');
  });
});
