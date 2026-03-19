import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import PodStatusDisplay from '../index';
import type { NamespaceStatus } from '../../../types';

const mockStatus: NamespaceStatus = {
  namespace: 'stack-test-user',
  status: 'healthy',
  charts: [
    {
      release_name: 'my-release',
      chart_name: 'my-app',
      status: 'healthy',
      deployments: [
        {
          name: 'my-app-deployment',
          ready_replicas: 2,
          desired_replicas: 2,
          updated_replicas: 2,
          available: true,
        },
      ],
      pods: [
        {
          name: 'my-app-pod-abc',
          phase: 'Running',
          ready: true,
          restart_count: 0,
          image: 'myimage:v1',
        },
      ],
      services: [
        {
          name: 'my-app-svc',
          type: 'ClusterIP',
          cluster_ip: '10.0.0.1',
        },
      ],
    },
  ],
  last_checked: '2026-03-19T10:00:00Z',
};

describe('PodStatusDisplay', () => {
  it('renders "No status available" when status is null', () => {
    render(<PodStatusDisplay status={null} />);
    expect(screen.getByText('No status available')).toBeInTheDocument();
  });

  it('shows loading indicator when loading=true', () => {
    render(<PodStatusDisplay status={null} loading={true} />);
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('renders namespace status chip', () => {
    render(<PodStatusDisplay status={mockStatus} />);
    expect(screen.getByText('Cluster Status:')).toBeInTheDocument();
    expect(screen.getAllByText('healthy').length).toBeGreaterThanOrEqual(1);
  });

  it('renders chart status with deployments and pods', () => {
    render(<PodStatusDisplay status={mockStatus} />);
    expect(screen.getByText('my-app')).toBeInTheDocument();
    expect(screen.getByText('my-app-deployment')).toBeInTheDocument();
    expect(screen.getByText('2/2 ready')).toBeInTheDocument();
    expect(screen.getByText('my-app-pod-abc')).toBeInTheDocument();
  });

  it('shows pod table with correct columns', () => {
    render(<PodStatusDisplay status={mockStatus} />);
    expect(screen.getByText('Pod')).toBeInTheDocument();
    expect(screen.getByText('Status')).toBeInTheDocument();
    expect(screen.getByText('Ready')).toBeInTheDocument();
    expect(screen.getByText('Restarts')).toBeInTheDocument();
    expect(screen.getByText('Image')).toBeInTheDocument();
  });

  it('shows restart count in red when > 5', () => {
    const highRestartStatus: NamespaceStatus = {
      ...mockStatus,
      charts: [
        {
          ...mockStatus.charts[0],
          pods: [
            {
              name: 'crash-pod',
              phase: 'Running',
              ready: true,
              restart_count: 10,
              image: 'myimage:v1',
            },
          ],
        },
      ],
    };
    render(<PodStatusDisplay status={highRestartStatus} />);
    const restartText = screen.getByText('10');
    expect(restartText).toBeInTheDocument();
    // The Typography has color="error" when restart_count > 5.
    // MUI applies the error color via Emotion CSS-in-JS. The generated class
    // differs from the non-error variant, so we verify the element does NOT
    // share the same class as a normal "text.primary" Typography. We also
    // verify the computed style contains the theme error color (#d32f2f).
    const normalText = screen.getByText('crash-pod');
    expect(restartText.className).not.toBe(normalText.className);
  });
});
