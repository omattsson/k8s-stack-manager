import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
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
          container_states: [],
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
              container_states: [],
            },
          ],
        },
      ],
    };
    render(<PodStatusDisplay status={highRestartStatus} />);
    const restartText = screen.getByText('10');
    expect(restartText).toBeInTheDocument();
    const normalText = screen.getByText('crash-pod');
    expect(restartText.className).not.toBe(normalText.className);
  });

  describe('Container States', () => {
    it('shows expand button when pod has container states with reasons', () => {
      const statusWithContainers: NamespaceStatus = {
        ...mockStatus,
        charts: [
          {
            ...mockStatus.charts[0],
            pods: [
              {
                name: 'crash-pod',
                phase: 'Running',
                ready: false,
                restart_count: 5,
                image: 'myimage:v1',
                container_states: [
                  {
                    name: 'main',
                    state: 'waiting',
                    reason: 'CrashLoopBackOff',
                    message: 'back-off 5m0s restarting',
                    restart_count: 5,
                    ready: false,
                    image: 'myimage:v1',
                  },
                ],
              },
            ],
          },
        ],
      };
      render(<PodStatusDisplay status={statusWithContainers} />);
      expect(screen.getByRole('button', { name: 'Expand container details' })).toBeInTheDocument();
    });

    it('shows container state details when expanded', async () => {
      const user = userEvent.setup();
      const statusWithContainers: NamespaceStatus = {
        ...mockStatus,
        charts: [
          {
            ...mockStatus.charts[0],
            pods: [
              {
                name: 'crash-pod',
                phase: 'Running',
                ready: false,
                restart_count: 5,
                image: 'myimage:v1',
                container_states: [
                  {
                    name: 'main',
                    state: 'waiting',
                    reason: 'CrashLoopBackOff',
                    message: 'back-off 5m0s restarting',
                    restart_count: 5,
                    ready: false,
                    image: 'myimage:v1',
                  },
                ],
              },
            ],
          },
        ],
      };
      render(<PodStatusDisplay status={statusWithContainers} />);
      await user.click(screen.getByRole('button', { name: 'Expand container details' }));
      expect(screen.getByText('Container States')).toBeInTheDocument();
      expect(screen.getByText('main')).toBeInTheDocument();
      expect(screen.getByText('CrashLoopBackOff')).toBeInTheDocument();
      expect(screen.getByText('waiting')).toBeInTheDocument();
    });

    it('shows exit code for terminated containers', async () => {
      const user = userEvent.setup();
      const statusWithTerminated: NamespaceStatus = {
        ...mockStatus,
        charts: [
          {
            ...mockStatus.charts[0],
            pods: [
              {
                name: 'oom-pod',
                phase: 'Running',
                ready: false,
                restart_count: 3,
                image: 'myimage:v1',
                container_states: [
                  {
                    name: 'worker',
                    state: 'terminated',
                    reason: 'OOMKilled',
                    restart_count: 3,
                    ready: false,
                    image: 'myimage:v1',
                    exit_code: 137,
                  },
                ],
              },
            ],
          },
        ],
      };
      render(<PodStatusDisplay status={statusWithTerminated} />);
      await user.click(screen.getByRole('button', { name: 'Expand container details' }));
      expect(screen.getByText('OOMKilled')).toBeInTheDocument();
      expect(screen.getByText('(exit code: 137)')).toBeInTheDocument();
    });

    it('does not show expand button when container states have no reasons', () => {
      const statusNoReasons: NamespaceStatus = {
        ...mockStatus,
        charts: [
          {
            ...mockStatus.charts[0],
            pods: [
              {
                name: 'healthy-pod',
                phase: 'Running',
                ready: true,
                restart_count: 0,
                image: 'myimage:v1',
                container_states: [
                  {
                    name: 'main',
                    state: 'running',
                    restart_count: 0,
                    ready: true,
                    image: 'myimage:v1',
                  },
                ],
              },
            ],
          },
        ],
      };
      render(<PodStatusDisplay status={statusNoReasons} />);
      expect(screen.queryByRole('button', { name: 'Expand container details' })).not.toBeInTheDocument();
    });
  });

  describe('Warning Events', () => {
    it('shows warning events section when events exist', () => {
      const statusWithEvents: NamespaceStatus = {
        ...mockStatus,
        events: [
          {
            type: 'Warning',
            reason: 'FailedScheduling',
            message: 'Insufficient cpu',
            object: 'pod/my-pod',
            count: 3,
            first_seen: '2026-03-19T09:00:00Z',
            last_seen: '2026-03-19T10:00:00Z',
          },
        ],
      };
      render(<PodStatusDisplay status={statusWithEvents} />);
      expect(screen.getByText('Recent Warnings')).toBeInTheDocument();
      expect(screen.getByText('FailedScheduling')).toBeInTheDocument();
      expect(screen.getByText('Insufficient cpu')).toBeInTheDocument();
      expect(screen.getByText('(pod/my-pod, x3)')).toBeInTheDocument();
    });

    it('does not show events section when no warning events', () => {
      const statusOnlyNormal: NamespaceStatus = {
        ...mockStatus,
        events: [
          {
            type: 'Normal',
            reason: 'Scheduled',
            message: 'Successfully assigned',
            object: 'pod/my-pod',
            count: 1,
            first_seen: '2026-03-19T09:00:00Z',
            last_seen: '2026-03-19T10:00:00Z',
          },
        ],
      };
      render(<PodStatusDisplay status={statusOnlyNormal} />);
      expect(screen.queryByText('Recent Warnings')).not.toBeInTheDocument();
    });

    it('does not show events section when events array is empty', () => {
      const statusEmptyEvents: NamespaceStatus = {
        ...mockStatus,
        events: [],
      };
      render(<PodStatusDisplay status={statusEmptyEvents} />);
      expect(screen.queryByText('Recent Warnings')).not.toBeInTheDocument();
    });

    it('limits warning events to 10', () => {
      const manyEvents = Array.from({ length: 15 }, (_, i) => ({
        type: 'Warning' as const,
        reason: `Reason${i}`,
        message: `Message ${i}`,
        object: `pod/pod-${i}`,
        count: 1,
        first_seen: '2026-03-19T09:00:00Z',
        last_seen: '2026-03-19T10:00:00Z',
      }));
      const statusManyEvents: NamespaceStatus = {
        ...mockStatus,
        events: manyEvents,
      };
      render(<PodStatusDisplay status={statusManyEvents} />);
      expect(screen.getByText('Recent Warnings')).toBeInTheDocument();
      // Should show first 10
      expect(screen.getByText('Reason0')).toBeInTheDocument();
      expect(screen.getByText('Reason9')).toBeInTheDocument();
      // Should not show 11th+
      expect(screen.queryByText('Reason10')).not.toBeInTheDocument();
    });
  });
});
