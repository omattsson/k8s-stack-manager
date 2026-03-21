import { useState, useEffect } from 'react';

interface CountdownResult {
  remaining: string;
  isWarning: boolean;
  isCritical: boolean;
  isExpired: boolean;
}

const formatRemaining = (ms: number): string => {
  if (ms <= 0) return 'Expired';
  const totalMinutes = Math.floor(ms / 60_000);
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  if (hours > 0) return `${hours}h ${minutes}m`;
  return `${minutes}m`;
};

const useCountdown = (expiresAt: string | undefined | null): CountdownResult | null => {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (!expiresAt) return;
    const interval = setInterval(() => setNow(Date.now()), 60_000);
    return () => clearInterval(interval);
  }, [expiresAt]);

  if (!expiresAt) return null;

  const expiryMs = new Date(expiresAt).getTime();
  const diffMs = expiryMs - now;

  return {
    remaining: formatRemaining(diffMs),
    isWarning: diffMs > 0 && diffMs <= 60 * 60_000,   // < 1 hour
    isCritical: diffMs > 0 && diffMs <= 30 * 60_000,  // < 30 minutes
    isExpired: diffMs <= 0,
  };
};

export default useCountdown;
