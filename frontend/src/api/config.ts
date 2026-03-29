export const API_BASE_URL = process.env.NODE_ENV === 'production' ? '/api' : 'http://localhost:8081';

export const endpoints = {
  health: {
    live: `${API_BASE_URL}/health/live`,
    ready: `${API_BASE_URL}/health/ready`,
  }
};

export const axiosConfig = {
  baseURL: API_BASE_URL,
  headers: {
    'Content-Type': 'application/json',
  },
};

// WebSocket base URL. In production, derived from the current page origin;
// in development, points directly at the backend dev server.
export const WS_BASE_URL =
  process.env.NODE_ENV === 'production'
    ? `${globalThis.location.protocol === 'https:' ? 'wss' : 'ws'}://${globalThis.location.host}`
    : 'ws://localhost:8081';
