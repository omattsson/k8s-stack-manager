import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Routes, Route } from 'react-router-dom';
import Instantiate from '../Instantiate';

const mockNavigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: () => mockNavigate,
  };
});

vi.mock('../../../api/client', () => ({
  templateService: {
    get: vi.fn(),
    instantiate: vi.fn(),
  },
}));

vi.mock('../../../components/YamlEditor', () => ({
  default: (props: { label?: string; value: string; readOnly?: boolean; onChange?: (val: string) => void }) => (
    <div data-testid="yaml-editor">
      <span>{props.label}</span>
      <pre>{props.value}</pre>
      {props.readOnly && <span>read-only</span>}
      {!props.readOnly && props.onChange && (
        <button type="button" onClick={() => props.onChange!('new: value')}>change-yaml</button>
      )}
    </div>
  ),
}));

import { templateService } from '../../../api/client';

const mockTemplate = {
  id: 't1',
  name: 'Web Stack Template',
  description: 'A web stack',
  category: 'Web',
  version: '1.0',
  owner_id: 'user1',
  default_branch: 'main',
  is_published: true,
  created_at: '',
  updated_at: '',
  charts: [
    {
      id: 'tc1',
      stack_template_id: 't1',
      chart_name: 'frontend',
      repository_url: 'https://charts.example.com',
      source_repo_url: '',
      chart_path: 'charts/frontend',
      chart_version: '1.0.0',
      default_values: 'replicaCount: 2',
      locked_values: 'image: nginx',
      deploy_order: 1,
      required: true,
      created_at: '',
    },
    {
      id: 'tc2',
      stack_template_id: 't1',
      chart_name: 'monitoring',
      repository_url: '',
      source_repo_url: '',
      chart_path: '',
      chart_version: '',
      default_values: 'enabled: true',
      locked_values: '',
      deploy_order: 2,
      required: false,
      created_at: '',
    },
  ],
};

describe('Templates Instantiate', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('shows loading spinner while fetching template', () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockReturnValue(new Promise(() => {}));
    render(
      <MemoryRouter initialEntries={['/templates/t1/use']}>
        <Routes>
          <Route path="/templates/:id/use" element={<Instantiate />} />
        </Routes>
      </MemoryRouter>
    );
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
  });

  it('displays template data and pre-filled form', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);
    render(
      <MemoryRouter initialEntries={['/templates/t1/use']}>
        <Routes>
          <Route path="/templates/:id/use" element={<Instantiate />} />
        </Routes>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByText(/Use Template: Web Stack Template/)).toBeInTheDocument();
    });
    // Pre-filled definition name
    expect(screen.getByDisplayValue('Web Stack Template - My Stack')).toBeInTheDocument();
    // Chart names displayed
    expect(screen.getByText('frontend')).toBeInTheDocument();
    expect(screen.getByText('monitoring')).toBeInTheDocument();
    // Required badge
    expect(screen.getByText('Required')).toBeInTheDocument();
  });

  it('shows error alert when template fetch fails', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockRejectedValue(new Error('Not found'));
    render(
      <MemoryRouter initialEntries={['/templates/t1/use']}>
        <Routes>
          <Route path="/templates/:id/use" element={<Instantiate />} />
        </Routes>
      </MemoryRouter>
    );
    await waitFor(() => {
      expect(screen.getByRole('alert')).toBeInTheDocument();
      expect(screen.getByText('Failed to load template')).toBeInTheDocument();
    });
  });

  it('creates stack definition on instantiate', async () => {
    const user = userEvent.setup();
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);
    (templateService.instantiate as ReturnType<typeof vi.fn>).mockResolvedValue({
      id: 'def-new',
      name: 'Web Stack Template - My Stack',
    });

    render(
      <MemoryRouter initialEntries={['/templates/t1/use']}>
        <Routes>
          <Route path="/templates/:id/use" element={<Instantiate />} />
        </Routes>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText(/Use Template/)).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /create stack definition/i }));

    await waitFor(() => {
      expect(templateService.instantiate).toHaveBeenCalledWith('t1', expect.objectContaining({
        name: 'Web Stack Template - My Stack',
      }));
    });
    expect(mockNavigate).toHaveBeenCalledWith('/stack-definitions/def-new/edit');
  });

  it('explains a 409 for a template without a published version', async () => {
    const user = userEvent.setup();
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);
    (templateService.instantiate as ReturnType<typeof vi.fn>).mockRejectedValue({
      response: { status: 409, data: { error: 'Template has no published version' } },
    });

    render(
      <MemoryRouter initialEntries={['/templates/t1/use']}>
        <Routes>
          <Route path="/templates/:id/use" element={<Instantiate />} />
        </Routes>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText(/Use Template/)).toBeInTheDocument();
    });
    await user.click(screen.getByRole('button', { name: /create stack definition/i }));

    expect(await screen.findByText(/has no published version\. ask the template owner to publish/i)).toBeInTheDocument();
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it('warns up front and blocks submit when the template has no published version', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...mockTemplate,
      published_version: null,
      published_charts: null,
    });
    render(
      <MemoryRouter initialEntries={['/templates/t1/use']}>
        <Routes>
          <Route path="/templates/:id/use" element={<Instantiate />} />
        </Routes>
      </MemoryRouter>
    );
    expect(await screen.findByText(/has no published version/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /create stack definition/i })).toBeDisabled();
  });

  it('shows the published charts and keys overrides by chart name', async () => {
    const user = userEvent.setup();
    const workingChart = mockTemplate.charts[0];
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...mockTemplate,
      published_version: '1.0.0',
      published_charts: [
        { ...workingChart, id: 'pub-1', chart_name: 'released-chart', default_values: 'released: true' },
      ],
    });
    (templateService.instantiate as ReturnType<typeof vi.fn>).mockResolvedValue({ id: 'def-new' });

    render(
      <MemoryRouter initialEntries={['/templates/t1/use']}>
        <Routes>
          <Route path="/templates/:id/use" element={<Instantiate />} />
        </Routes>
      </MemoryRouter>
    );

    expect(await screen.findByText('released-chart')).toBeInTheDocument();
    expect(screen.queryByText(workingChart.chart_name)).not.toBeInTheDocument();
    expect(screen.getByText('released: true')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: /change-yaml/i }));
    await user.click(screen.getByRole('button', { name: /create stack definition/i }));

    await waitFor(() => {
      expect(templateService.instantiate).toHaveBeenCalledWith('t1', expect.objectContaining({
        chart_overrides: { 'released-chart': 'new: value' },
      }));
    });
  });

  it('warns and blocks submit when the template is unpublished', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue({
      ...mockTemplate,
      is_published: false,
      published_version: '1.0.0',
      published_charts: mockTemplate.charts,
    });
    render(
      <MemoryRouter initialEntries={['/templates/t1/use']}>
        <Routes>
          <Route path="/templates/:id/use" element={<Instantiate />} />
        </Routes>
      </MemoryRouter>
    );
    expect(await screen.findByText(/this template is unpublished/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /create stack definition/i })).toBeDisabled();
  });

  it('shows the release version the definition is created from', async () => {
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue({ ...mockTemplate, published_version: '1.2.0' });
    render(
      <MemoryRouter initialEntries={['/templates/t1/use']}>
        <Routes>
          <Route path="/templates/:id/use" element={<Instantiate />} />
        </Routes>
      </MemoryRouter>
    );
    expect(await screen.findByText('Creates a definition from version 1.2.0.')).toBeInTheDocument();
  });

  it('navigates back when Cancel is clicked', async () => {
    const user = userEvent.setup();
    (templateService.get as ReturnType<typeof vi.fn>).mockResolvedValue(mockTemplate);

    render(
      <MemoryRouter initialEntries={['/templates/t1/use']}>
        <Routes>
          <Route path="/templates/:id/use" element={<Instantiate />} />
        </Routes>
      </MemoryRouter>
    );

    await waitFor(() => {
      expect(screen.getByText(/Use Template/)).toBeInTheDocument();
    });

    await user.click(screen.getByRole('button', { name: /cancel/i }));
    expect(mockNavigate).toHaveBeenCalledWith('/templates/t1');
  });
});
