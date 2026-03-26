const STORAGE_KEY = 'recentTemplates';
const MAX_RECENT = 5;

interface RecentTemplate {
  id: string;
  name: string;
}

interface StoredRecentTemplate extends RecentTemplate {
  usedAt: string;
}

function isStoredRecentTemplate(value: unknown): value is StoredRecentTemplate {
  if (!value || typeof value !== 'object') return false;
  const v = value as Partial<StoredRecentTemplate>;
  return typeof v.id === 'string' && typeof v.name === 'string' && typeof v.usedAt === 'string';
}

export function trackRecentTemplate(template: RecentTemplate): void {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    let recent: StoredRecentTemplate[] = [];
    if (stored) {
      const parsed = JSON.parse(stored);
      if (Array.isArray(parsed)) {
        recent = parsed.filter(isStoredRecentTemplate);
      }
    }
    const entry: StoredRecentTemplate = { ...template, usedAt: new Date().toISOString() };
    recent = [entry, ...recent.filter(t => t.id !== template.id)].slice(0, MAX_RECENT);
    localStorage.setItem(STORAGE_KEY, JSON.stringify(recent));
  } catch {
    // Ignore localStorage errors
  }
}
