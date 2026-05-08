const STORAGE_KEY = 'setupWizardDismissed';

export function isSetupWizardDismissed(): boolean {
  try {
    return localStorage.getItem(STORAGE_KEY) === 'true';
  } catch {
    return false;
  }
}

export function dismissSetupWizard(): void {
  try {
    localStorage.setItem(STORAGE_KEY, 'true');
  } catch {
    // Ignore localStorage errors
  }
}

export function resetSetupWizard(): void {
  try {
    localStorage.removeItem(STORAGE_KEY);
  } catch {
    // Ignore localStorage errors
  }
}
