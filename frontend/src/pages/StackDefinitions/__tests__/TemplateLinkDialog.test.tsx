import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import TemplateLinkDialog from '../TemplateLinkDialog';

vi.mock('../../../api/client', () => ({
  definitionService: {
    setTemplateLink: vi.fn(),
  },
  templateService: {
    listAll: vi.fn(),
    listVersions: vi.fn(),
  },
}));

import { definitionService, templateService } from '../../../api/client';

const mocked = <T,>(fn: T) => fn as unknown as ReturnType<typeof vi.fn>;

const templates = [
  { id: 't1', name: 'Web stack', is_published: true },
  { id: 't2', name: 'Draft stack', is_published: false },
];
const versions = [
  { id: 'v2', template_id: 't1', version: '1.1.0' },
  { id: 'v1', template_id: 't1', version: '1.0.0' },
];

const renderDialog = (props: Partial<Parameters<typeof TemplateLinkDialog>[0]> = {}) => {
  const onLinked = vi.fn();
  const onClose = vi.fn();
  render(
    <TemplateLinkDialog definitionId="d1" open onClose={onClose} onLinked={onLinked} {...props} />,
  );
  return { onLinked, onClose };
};

describe('TemplateLinkDialog', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('lists only published templates and links the latest version', async () => {
    const user = userEvent.setup();
    mocked(templateService.listAll).mockResolvedValue(templates);
    mocked(templateService.listVersions).mockResolvedValue(versions);
    const updated = { id: 'd1', source_template_id: 't1', source_template_version: '1.1.0' };
    mocked(definitionService.setTemplateLink).mockResolvedValue(updated);
    const { onLinked } = renderDialog();

    await user.click(await screen.findByRole('combobox', { name: 'Template' }));
    const listbox = within(screen.getByRole('listbox'));
    expect(listbox.getByText('Web stack')).toBeInTheDocument();
    expect(listbox.queryByText('Draft stack')).not.toBeInTheDocument();
    await user.click(listbox.getByText('Web stack'));

    await waitFor(() => expect(templateService.listVersions).toHaveBeenCalledWith('t1'));
    expect(await screen.findByText('Latest (1.1.0)')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Save link' }));

    await waitFor(() => expect(definitionService.setTemplateLink).toHaveBeenCalledWith('d1', 't1', ''));
    expect(onLinked).toHaveBeenCalledWith(updated);
    expect(screen.queryByRole('button', { name: 'Remove link' })).not.toBeInTheDocument();
  });

  it('links an older version', async () => {
    const user = userEvent.setup();
    mocked(templateService.listAll).mockResolvedValue(templates);
    mocked(templateService.listVersions).mockResolvedValue(versions);
    mocked(definitionService.setTemplateLink).mockResolvedValue({ id: 'd1' });
    renderDialog({ currentTemplateId: 't1', currentVersion: '1.1.0' });

    await waitFor(() => expect(templateService.listVersions).toHaveBeenCalledWith('t1'));
    await user.click(await screen.findByRole('combobox', { name: 'Version' }));
    await user.click(within(screen.getByRole('listbox')).getByText('1.0.0'));
    await user.click(screen.getByRole('button', { name: 'Save link' }));

    await waitFor(() => expect(definitionService.setTemplateLink).toHaveBeenCalledWith('d1', 't1', '1.0.0'));
  });

  it('removes the link', async () => {
    const user = userEvent.setup();
    mocked(templateService.listAll).mockResolvedValue(templates);
    mocked(templateService.listVersions).mockResolvedValue(versions);
    mocked(definitionService.setTemplateLink).mockResolvedValue({ id: 'd1' });
    const { onLinked } = renderDialog({ currentTemplateId: 't1', currentVersion: '1.0.0' });

    expect(await screen.findByText(/Current link: Web stack/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Remove link' }));

    await waitFor(() => expect(definitionService.setTemplateLink).toHaveBeenCalledWith('d1', '', ''));
    expect(onLinked).toHaveBeenCalledWith({ id: 'd1' });
  });

  it('shows the server error', async () => {
    const user = userEvent.setup();
    mocked(templateService.listAll).mockResolvedValue(templates);
    mocked(templateService.listVersions).mockResolvedValue(versions);
    mocked(definitionService.setTemplateLink).mockRejectedValue({
      isAxiosError: true,
      response: { status: 409, data: { error: 'Template has no published version' } },
    });
    const { onLinked } = renderDialog({ currentTemplateId: 't1' });

    await waitFor(() => expect(templateService.listVersions).toHaveBeenCalled());
    await user.click(screen.getByRole('button', { name: 'Save link' }));

    expect(await screen.findByText('Template has no published version')).toBeInTheDocument();
    expect(onLinked).not.toHaveBeenCalled();
  });

  it('warns when the linked template is not published now', async () => {
    mocked(templateService.listAll).mockResolvedValue(templates);
    mocked(templateService.listVersions).mockResolvedValue([]);
    renderDialog({ currentTemplateId: 't2' });

    expect(await screen.findByText(/The linked template is not published now/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save link' })).toBeDisabled();
  });
});
