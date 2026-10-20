import { describe, it, expect, vi, afterEach } from 'vitest';
import { act, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import InstanceQuotaSection from '../index';
import { NotificationProvider } from '../../../context/NotificationContext';
import type { EffectiveQuota, EffectiveQuotaValues, QuotaValueSource } from '../../../types';

const authState = vi.hoisted(() => ({ user: { id: 'owner-1', username: 'olle', role: 'user' } }));

vi.mock('../../../context/AuthContext', () => ({
  useAuth: () => ({ user: authState.user, isAuthenticated: true, isLoading: false }),
}));

vi.mock('../../../api/client', () => ({
  instanceService: {
    getEffectiveQuota: vi.fn(),
    getQuotaOverride: vi.fn(),
    setQuotaOverride: vi.fn(),
    deleteQuotaOverride: vi.fn(),
  },
}));

import { instanceService } from '../../../api/client';

type MockFn = ReturnType<typeof vi.fn>;

const emptyValues: EffectiveQuotaValues = {
  cpu_request: '',
  cpu_limit: '',
  memory_request: '',
  memory_limit: '',
  storage_limit: '',
  ephemeral_storage_limit: '',
  container_cpu_request: '',
  container_cpu_limit: '',
  container_cpu_max: '',
  container_memory_request: '',
  container_memory_limit: '',
  container_memory_max: '',
  container_ephemeral_storage_request: '',
  container_ephemeral_storage_limit: '',
  pod_limit: 0,
};

const clusterQuota = {
  id: 'q1',
  cluster_id: 'cl-1',
  cpu_request: '1',
  cpu_limit: '2',
  memory_request: '1Gi',
  memory_limit: '2Gi',
  storage_limit: '',
  pod_limit: 10,
  container_cpu_max: '1',
};

const override = {
  id: 'o1',
  stack_instance_id: 'inst-1',
  cpu_request: '',
  cpu_limit: '1500m',
  memory_request: '',
  memory_limit: '',
  storage_limit: '',
  pod_limit: null,
};

/** Effective quota of clusterQuota merged with override (cpu_limit from the override). */
const mergedQuota = (canModify: boolean, withOverride = true): EffectiveQuota => {
  const effective = {
    ...emptyValues,
    cpu_request: '1',
    cpu_limit: withOverride ? '1500m' : '2',
    memory_request: '1Gi',
    memory_limit: '2Gi',
    container_cpu_max: '1',
    pod_limit: 10,
  };
  const sources = Object.fromEntries(
    Object.entries(effective).map(([field, value]) => [field, value === '' ? 'none' : 'cluster']),
  ) as Record<keyof EffectiveQuotaValues, QuotaValueSource>;
  if (withOverride) sources.cpu_limit = 'override';
  return {
    cluster_id: 'cl-1',
    effective,
    cluster: clusterQuota,
    override: withOverride ? override : null,
    sources,
    can_modify: canModify,
  };
};

const noQuota = (canModify: boolean): EffectiveQuota => ({
  cluster_id: 'cl-1',
  effective: emptyValues,
  cluster: null,
  override: null,
  sources: Object.fromEntries(Object.keys(emptyValues).map((f) => [f, 'none'])) as Record<keyof EffectiveQuotaValues, QuotaValueSource>,
  can_modify: canModify,
});

const httpError = (status: number, message: string) => ({ response: { status, data: { error: message } } });

const renderSection = () =>
  render(
    <NotificationProvider>
      <InstanceQuotaSection instanceId="inst-1" />
    </NotificationProvider>,
  );

const field = (name: string) => screen.getByRole('group', { name });

/** Open the edit dialog and wait until the override is loaded. */
const openEditDialog = async (user: ReturnType<typeof userEvent.setup>) => {
  await user.click(await screen.findByRole('button', { name: 'Edit override' }));
  const dialog = await screen.findByRole('dialog', { name: 'Quota override' });
  await waitFor(() => expect(within(dialog).getByLabelText('CPU Limit')).toHaveValue('1500m'));
  return dialog;
};

describe('InstanceQuotaSection', () => {
  afterEach(() => {
    vi.clearAllMocks();
    authState.user = { id: 'owner-1', username: 'olle', role: 'user' };
  });

  it('shows the effective values with their source', async () => {
    (instanceService.getEffectiveQuota as MockFn).mockResolvedValue(mergedQuota(true));
    renderSection();

    expect(await screen.findByRole('heading', { name: 'Resource quota' })).toBeInTheDocument();
    expect(instanceService.getEffectiveQuota).toHaveBeenCalledWith('inst-1');
    expect(within(field('CPU Limit')).getByText('1.5 cores (1500m)')).toBeInTheDocument();
    expect(within(field('CPU Limit')).getByText('Override')).toBeInTheDocument();
    expect(within(field('Memory Limit')).getByText('2 GiB')).toBeInTheDocument();
    expect(within(field('Memory Limit')).getByText('Cluster')).toBeInTheDocument();
    expect(within(field('CPU Request')).getByText('1 core')).toBeInTheDocument();
    expect(within(field('Pod Limit')).getByText('10')).toBeInTheDocument();
    expect(within(field('Container CPU Max')).getByText('Cluster')).toBeInTheDocument();
    // A field without a value is not listed.
    expect(screen.queryByRole('group', { name: 'Storage Limit' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Edit override' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Remove override' })).toBeInTheDocument();
  });

  it('shows "No limit" for an override pod_limit of 0', async () => {
    const quota = mergedQuota(true);
    quota.effective.pod_limit = 0;
    quota.sources.pod_limit = 'override';
    (instanceService.getEffectiveQuota as MockFn).mockResolvedValue(quota);
    renderSection();

    expect(await screen.findByText('No limit')).toBeInTheDocument();
    expect(within(field('Pod Limit')).getByText('Override')).toBeInTheDocument();
  });

  it('is read-only for a user who cannot modify the instance', async () => {
    (instanceService.getEffectiveQuota as MockFn).mockResolvedValue(mergedQuota(false));
    renderSection();

    expect(await screen.findByText('1.5 cores (1500m)')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Edit override' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Remove override' })).not.toBeInTheDocument();
  });

  it('is hidden when no value applies and the user cannot modify the instance', async () => {
    (instanceService.getEffectiveQuota as MockFn).mockResolvedValue(noQuota(false));
    renderSection();

    await waitFor(() => expect(instanceService.getEffectiveQuota).toHaveBeenCalled());
    await act(() => Promise.resolve());
    expect(screen.queryByRole('heading', { name: 'Resource quota' })).not.toBeInTheDocument();
    expect(screen.queryByText('No quota applies to this stack.')).not.toBeInTheDocument();
  });

  it('offers an override when no value applies and the user can modify the instance', async () => {
    (instanceService.getEffectiveQuota as MockFn).mockResolvedValue(noQuota(true));
    renderSection();

    expect(await screen.findByText('No quota applies to this stack.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Edit override' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Remove override' })).not.toBeInTheDocument();
  });

  it('shows a load error', async () => {
    (instanceService.getEffectiveQuota as MockFn).mockRejectedValue(httpError(500, 'Internal server error'));
    renderSection();

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Failed to load the resource quota (HTTP 500: Internal server error)',
    );
  });

  it('edits the override and reloads the section', async () => {
    const user = userEvent.setup();
    authState.user = { id: 'devops-1', username: 'dora', role: 'devops' };
    (instanceService.getEffectiveQuota as MockFn).mockResolvedValue(mergedQuota(true));
    (instanceService.getQuotaOverride as MockFn).mockResolvedValue(override);
    (instanceService.setQuotaOverride as MockFn).mockResolvedValue({ ...override, memory_limit: '4Gi' });
    renderSection();

    const dialog = await openEditDialog(user);
    expect(instanceService.getQuotaOverride).toHaveBeenCalledWith('inst-1');
    // devops sees the cluster value without a cap.
    expect(within(dialog).getByText('Cluster quota: 2Gi')).toBeInTheDocument();

    await user.type(within(dialog).getByLabelText('Memory Limit'), ' 4Gi ');
    await user.type(within(dialog).getByLabelText('Pod Limit'), '20');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(instanceService.setQuotaOverride).toHaveBeenCalledTimes(1));
    expect(instanceService.setQuotaOverride).toHaveBeenCalledWith('inst-1', expect.objectContaining({
      cpu_limit: '1500m',
      memory_limit: '4Gi',
      cpu_request: '',
      container_cpu_max: '',
      pod_limit: 20,
    }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Quota override' })).not.toBeInTheDocument());
    await waitFor(() => expect(instanceService.getEffectiveQuota).toHaveBeenCalledTimes(2));
    expect(await screen.findByText('Quota override saved')).toBeInTheDocument();
  });

  it('sends pod_limit null when the field is empty', async () => {
    const user = userEvent.setup();
    (instanceService.getEffectiveQuota as MockFn).mockResolvedValue(mergedQuota(true));
    (instanceService.getQuotaOverride as MockFn).mockResolvedValue({ ...override, pod_limit: 5 });
    (instanceService.setQuotaOverride as MockFn).mockResolvedValue(override);
    renderSection();

    const dialog = await openEditDialog(user);
    await user.clear(within(dialog).getByLabelText('Pod Limit'));
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(instanceService.setQuotaOverride).toHaveBeenCalledWith(
      'inst-1',
      expect.objectContaining({ cpu_limit: '1500m', pod_limit: null }),
    ));
  });

  it('shows the cluster value as the upper limit for the role user', async () => {
    const user = userEvent.setup();
    (instanceService.getEffectiveQuota as MockFn).mockResolvedValue(mergedQuota(true));
    (instanceService.getQuotaOverride as MockFn).mockResolvedValue(override);
    renderSection();

    const dialog = await openEditDialog(user);
    expect(within(dialog).getByText('At most 2 (cluster quota)')).toBeInTheDocument();
    expect(within(dialog).getByText('At most 2Gi (cluster quota)')).toBeInTheDocument();
    expect(within(dialog).getByText('At most 10 (cluster quota). 0 = no limit.')).toBeInTheDocument();
  });

  it.each([
    [400, 'cpu_request 3 (instance override) exceeds the effective cpu_limit 2 (cluster quota)'],
    [403, 'cpu_limit 3 exceeds the cluster quota 2; only admin or devops can set a higher quota'],
  ])('shows the server message of a %i answer in the dialog', async (status, message) => {
    const user = userEvent.setup();
    (instanceService.getEffectiveQuota as MockFn).mockResolvedValue(mergedQuota(true));
    (instanceService.getQuotaOverride as MockFn).mockResolvedValue(override);
    (instanceService.setQuotaOverride as MockFn).mockRejectedValue(httpError(status, message));
    renderSection();

    const dialog = await openEditDialog(user);
    await user.clear(within(dialog).getByLabelText('CPU Limit'));
    await user.type(within(dialog).getByLabelText('CPU Limit'), '3');
    await user.click(within(dialog).getByRole('button', { name: 'Save' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent(
      `Failed to save the quota override (HTTP ${status}: ${message})`,
    );
    // The dialog stays open with the input, and the section is not reloaded.
    expect(within(dialog).getByLabelText('CPU Limit')).toHaveValue('3');
    expect(instanceService.getEffectiveQuota).toHaveBeenCalledTimes(1);
  });

  it('blocks a save while a field is invalid', async () => {
    const user = userEvent.setup();
    (instanceService.getEffectiveQuota as MockFn).mockResolvedValue(mergedQuota(true));
    (instanceService.getQuotaOverride as MockFn).mockResolvedValue(override);
    renderSection();

    const dialog = await openEditDialog(user);
    await user.type(within(dialog).getByLabelText('CPU Request'), '2');
    expect(within(dialog).getByText('CPU request must not be higher than the CPU limit')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Save' })).toBeDisabled();
  });

  it('removes the override after a confirmation and reloads the section', async () => {
    const user = userEvent.setup();
    (instanceService.getEffectiveQuota as MockFn)
      .mockResolvedValueOnce(mergedQuota(true))
      .mockResolvedValueOnce(mergedQuota(true, false));
    (instanceService.deleteQuotaOverride as MockFn).mockResolvedValue(undefined);
    renderSection();

    await user.click(await screen.findByRole('button', { name: 'Remove override' }));
    const confirm = await screen.findByRole('dialog', { name: 'Remove the quota override' });
    expect(instanceService.deleteQuotaOverride).not.toHaveBeenCalled();
    await user.click(within(confirm).getByRole('button', { name: 'Remove' }));

    await waitFor(() => expect(instanceService.deleteQuotaOverride).toHaveBeenCalledWith('inst-1'));
    expect(await screen.findByText('Quota override removed')).toBeInTheDocument();
    await waitFor(() => expect(within(field('CPU Limit')).getByText('2 cores')).toBeInTheDocument());
    expect(within(field('CPU Limit')).getByText('Cluster')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Remove override' })).not.toBeInTheDocument();
  });

  it('shows the server message when the remove fails', async () => {
    const user = userEvent.setup();
    (instanceService.getEffectiveQuota as MockFn).mockResolvedValue(mergedQuota(true));
    (instanceService.deleteQuotaOverride as MockFn).mockRejectedValue(
      httpError(403, 'You are not allowed to modify this stack instance'),
    );
    renderSection();

    await user.click(await screen.findByRole('button', { name: 'Remove override' }));
    await user.click(within(await screen.findByRole('dialog', { name: 'Remove the quota override' })).getByRole('button', { name: 'Remove' }));

    expect(await screen.findByText(
      'Failed to remove the quota override (HTTP 403: You are not allowed to modify this stack instance)',
    )).toBeInTheDocument();
  });
});
