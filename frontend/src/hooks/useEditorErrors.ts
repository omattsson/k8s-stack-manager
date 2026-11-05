import { useCallback, useState } from 'react';

/**
 * Blocking errors of the YAML editors of a form, keyed `${chartIndex}:${field}` (#756).
 * An editor that unmounts does not report, so a removed chart needs `removeIndex`.
 */
export function useEditorErrors() {
  const [errors, setErrors] = useState<Record<string, string>>({});

  const report = useCallback((key: string, message: string | null) => {
    setErrors((prev) => {
      if ((prev[key] ?? null) === message) return prev;
      const next = { ...prev };
      if (message) next[key] = message;
      else delete next[key];
      return next;
    });
  }, []);

  /** Drop the errors of one chart and move the later charts up by one. */
  const removeIndex = useCallback((index: number) => {
    setErrors((prev) => {
      const next: Record<string, string> = {};
      for (const [key, message] of Object.entries(prev)) {
        const [rawIndex, field] = key.split(':');
        const i = Number(rawIndex);
        if (i === index) continue;
        next[i > index ? `${i - 1}:${field}` : key] = message;
      }
      return next;
    });
  }, []);

  return { errors, report, removeIndex };
}

export const EDITOR_ERROR_HINT = 'Fix the YAML errors before you save.';
