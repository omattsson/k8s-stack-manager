const STORAGE_KEY = 'recentTemplates';
const MAX_RECENT = 5;

interface RecentTemplate {
  id: string;
  name: string;
}

export function trackRecentTemplate(template: RecentTemplate): void {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    let recent: RecentTemplate[] = [];
    if (stored) {
      const parsed = JSON.parse(stored);
      if (Array.isArray(parsed)) {
        recent = parsed;
      }
    }
    recent = [template, ...recent.filter(t => t.id !== template.id)].slice(0, MAX_RECENT);
    localStorage.setItem(STORAGE_KEY, JSON.stringify(recent));
  } catch {
    // Ignore localStorage errors
  }
}
