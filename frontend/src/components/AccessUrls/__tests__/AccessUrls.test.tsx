import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import AccessUrls from '../index';
import type { NamespaceStatus } from '../../../types';
import { NotificationProvider } from '../../../context/NotificationContext';

describe('AccessUrls', () => {
  beforeEach(() => {
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn() },
      writable: true,
      configurable: true,
    });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it('renders ingress URLs with clickable links', () => {
    const status: NamespaceStatus = {
      namespace: 'stack-test',
      status: 'healthy',
      charts: [],
      ingresses: [
        { name: 'my-ingress', host: 'app.example.com', path: '/', tls: true, url: 'https://app.example.com' },
      ],
      last_checked: '2025-01-01T00:00:00Z',
    };

    render(<NotificationProvider><AccessUrls status={status} /></NotificationProvider>);

    expect(screen.getByText('Access URLs')).toBeInTheDocument();
    expect(screen.getByText('https://app.example.com')).toBeInTheDocument();
    expect(screen.getByText('Ingress')).toBeInTheDocument();

    const link = screen.getByRole('link', { name: 'https://app.example.com' });
    expect(link).toHaveAttribute('href', 'https://app.example.com');
    expect(link).toHaveAttribute('target', '_blank');
  });

  it('renders LoadBalancer external IPs', () => {
    const status: NamespaceStatus = {
      namespace: 'stack-test',
      status: 'healthy',
      charts: [
        {
          release_name: 'api-gateway',
          chart_name: 'api-gateway',
          status: 'healthy',
          deployments: [],
          pods: [],
          services: [
            {
              name: 'api-gateway',
              type: 'LoadBalancer',
              cluster_ip: '10.0.0.1',
              ports: ['8080/TCP'],
              external_ip: '20.1.2.3',
              ingress_hosts: [],
            },
          ],
        },
      ],
      last_checked: '2025-01-01T00:00:00Z',
    };

    render(<NotificationProvider><AccessUrls status={status} /></NotificationProvider>);

    expect(screen.getByText('LoadBalancer')).toBeInTheDocument();
    expect(screen.getByText('http://20.1.2.3:8080')).toBeInTheDocument();
  });

  it('renders port-forward command for ClusterIP services', () => {
    const status: NamespaceStatus = {
      namespace: 'stack-my-app',
      status: 'healthy',
      charts: [
        {
          release_name: 'database',
          chart_name: 'database',
          status: 'healthy',
          deployments: [],
          pods: [],
          services: [
            {
              name: 'database',
              type: 'ClusterIP',
              cluster_ip: '10.0.0.2',
              ports: ['5432/TCP'],
              ingress_hosts: [],
            },
          ],
        },
      ],
      last_checked: '2025-01-01T00:00:00Z',
    };

    render(<NotificationProvider><AccessUrls status={status} /></NotificationProvider>);

    expect(screen.getByText('ClusterIP')).toBeInTheDocument();
    expect(screen.getByText('kubectl port-forward svc/database 5432:5432 -n stack-my-app')).toBeInTheDocument();
  });

  it('renders NodePort services', () => {
    const status: NamespaceStatus = {
      namespace: 'stack-test',
      status: 'healthy',
      charts: [
        {
          release_name: 'web',
          chart_name: 'web',
          status: 'healthy',
          deployments: [],
          pods: [],
          services: [
            {
              name: 'web-svc',
              type: 'NodePort',
              cluster_ip: '10.0.0.3',
              ports: ['80/TCP'],
              node_ports: [30080],
              ingress_hosts: [],
            },
          ],
        },
      ],
      last_checked: '2025-01-01T00:00:00Z',
    };

    render(<NotificationProvider><AccessUrls status={status} /></NotificationProvider>);

    expect(screen.getByText('NodePort')).toBeInTheDocument();
    expect(screen.getByText('NodePort: 30080')).toBeInTheDocument();
  });

  it('returns null when no access entries exist', () => {
    const status: NamespaceStatus = {
      namespace: 'stack-test',
      status: 'healthy',
      charts: [],
      last_checked: '2025-01-01T00:00:00Z',
    };

    const { container } = render(<NotificationProvider><AccessUrls status={status} /></NotificationProvider>);
    expect(container.firstChild).toBeNull();
  });

  it('skips services that have ingress hosts', () => {
    const status: NamespaceStatus = {
      namespace: 'stack-test',
      status: 'healthy',
      charts: [
        {
          release_name: 'web',
          chart_name: 'web',
          status: 'healthy',
          deployments: [],
          pods: [],
          services: [
            {
              name: 'web-svc',
              type: 'ClusterIP',
              cluster_ip: '10.0.0.1',
              ports: ['80/TCP'],
              ingress_hosts: ['app.example.com'],
            },
          ],
        },
      ],
      ingresses: [
        { name: 'web-ingress', host: 'app.example.com', path: '/', tls: true, url: 'https://app.example.com' },
      ],
      last_checked: '2025-01-01T00:00:00Z',
    };

    render(<NotificationProvider><AccessUrls status={status} /></NotificationProvider>);

    // Should show the ingress but not a separate ClusterIP entry
    expect(screen.getByText('Ingress')).toBeInTheDocument();
    expect(screen.queryByText('ClusterIP')).not.toBeInTheDocument();
  });

  it('copies text to clipboard on copy button click', async () => {
    const user = userEvent.setup();
    const writeTextSpy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue();
    const status: NamespaceStatus = {
      namespace: 'stack-test',
      status: 'healthy',
      charts: [],
      ingresses: [
        { name: 'my-ingress', host: 'app.example.com', path: '/', tls: true, url: 'https://app.example.com' },
      ],
      last_checked: '2025-01-01T00:00:00Z',
    };

    render(<NotificationProvider><AccessUrls status={status} /></NotificationProvider>);

    await user.click(screen.getByRole('button', { name: /copy my-ingress/i }));
    expect(writeTextSpy).toHaveBeenCalledWith('https://app.example.com');
    writeTextSpy.mockRestore();
  });
});
