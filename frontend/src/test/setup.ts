import '@testing-library/jest-dom/vitest';

/**
 * Node.js 22+ ships a built-in `localStorage` (via `--localstorage-file`) that
 * lacks a proper `clear()` method and conflicts with jsdom's implementation.
 * Replace it with a Map-backed mock so all test files get a working localStorage.
 */
if (typeof globalThis.localStorage === 'undefined' || typeof globalThis.localStorage.clear !== 'function') {
  const store = new Map<string, string>();
  const mockLocalStorage: Storage = {
    getItem(key: string): string | null {
      return store.get(key) ?? null;
    },
    setItem(key: string, value: string): void {
      store.set(key, String(value));
    },
    removeItem(key: string): void {
      store.delete(key);
    },
    clear(): void {
      store.clear();
    },
    get length(): number {
      return store.size;
    },
    key(index: number): string | null {
      const keys = Array.from(store.keys());
      return keys[index] ?? null;
    },
  };
  Object.defineProperty(globalThis, 'localStorage', {
    value: mockLocalStorage,
    writable: true,
    configurable: true,
  });
}
