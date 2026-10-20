import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import SetupWizard from '../index';
import { StaticBrandingProvider } from '../../../context/BrandingContext';

const renderWizard = (props: Partial<React.ComponentProps<typeof SetupWizard>> = {}) =>
  render(
    <MemoryRouter>
      <SetupWizard
        hasClusters={false}
        hasTemplates={false}
        hasInstances={false}
        isAdmin={true}
        isDevOps={true}
        onDismiss={vi.fn()}
        {...props}
      />
    </MemoryRouter>,
  );

describe('SetupWizard', () => {
  afterEach(() => {
    vi.clearAllMocks();
  });

  it('renders welcome heading', () => {
    renderWizard();
    expect(screen.getByText('Welcome to K8s Stack Manager')).toBeInTheDocument();
  });

  it('uses the configured title in the welcome heading', () => {
    render(
      <StaticBrandingProvider value={{ title: 'Platform Portal', logoUrl: '/logo.svg', faviconUrl: '/favicon.svg', defaultTtlMinutes: 0, maxTtlDays: 30 }}>
        <MemoryRouter>
          <SetupWizard
            hasClusters={false}
            hasTemplates={false}
            hasInstances={false}
            isAdmin={true}
            isDevOps={true}
            onDismiss={vi.fn()}
          />
        </MemoryRouter>
      </StaticBrandingProvider>,
    );
    expect(screen.getByText('Welcome to Platform Portal')).toBeInTheDocument();
  });

  it('renders all three step labels', () => {
    renderWizard();
    expect(screen.getAllByText('Register a Cluster').length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText('Create a Template')).toBeInTheDocument();
    expect(screen.getByText('Deploy an Instance')).toBeInTheDocument();
  });

  it('shows cluster step as active when no clusters exist', () => {
    renderWizard({ hasClusters: false });
    expect(screen.getByRole('button', { name: 'Register a Cluster' })).toBeInTheDocument();
  });

  it('shows template step as active when clusters exist but no templates', () => {
    renderWizard({ hasClusters: true, hasTemplates: false });
    expect(screen.getByRole('button', { name: 'Create a Template' })).toBeInTheDocument();
  });

  it('shows instance step as active when clusters and templates exist', () => {
    renderWizard({ hasClusters: true, hasTemplates: true, hasInstances: false });
    expect(screen.getByRole('button', { name: 'Deploy an Instance' })).toBeInTheDocument();
  });

  it('shows step 3 of 3 with steps 1 and 2 complete when clusters and templates exist', () => {
    const { container } = renderWizard({ hasClusters: true, hasTemplates: true, hasInstances: false });
    expect(screen.getByText('Step 3 of 3')).toBeInTheDocument();
    const completed = Array.from(container.querySelectorAll('.MuiStepLabel-label.Mui-completed')).map((el) => el.textContent);
    expect(completed).toEqual(['Register a Cluster', 'Create a Template']);
  });

  it('opens the templates page from the deploy step, also for the role user', async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route
            path="/"
            element={(
              <SetupWizard
                hasClusters={true}
                hasTemplates={true}
                hasInstances={false}
                isAdmin={false}
                isDevOps={false}
                onDismiss={vi.fn()}
              />
            )}
          />
          <Route path="/templates" element={<div>Templates page</div>} />
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.queryByText(/Only administrators|Only DevOps users/)).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Deploy an Instance' }));
    expect(screen.getByText('Templates page')).toBeInTheDocument();
  });

  it('shows info alert for non-admin users on cluster step', () => {
    renderWizard({ isAdmin: false, hasClusters: false });
    expect(screen.getByText(/Only administrators can register clusters/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Register a Cluster' })).not.toBeInTheDocument();
  });

  it('shows action button for admin users on cluster step', () => {
    renderWizard({ isAdmin: true, hasClusters: false });
    expect(screen.getByRole('button', { name: 'Register a Cluster' })).toBeInTheDocument();
    expect(screen.queryByText(/Only administrators/)).not.toBeInTheDocument();
  });

  it('calls onDismiss when skip button is clicked', async () => {
    const user = userEvent.setup();
    const onDismiss = vi.fn();
    renderWizard({ onDismiss });
    await user.click(screen.getByText('Skip setup'));
    expect(onDismiss).toHaveBeenCalledOnce();
  });

  it('shows info alert for non-devops users on template step', () => {
    renderWizard({ hasClusters: true, hasTemplates: false, isDevOps: false });
    expect(screen.getByText(/Only DevOps users can create templates/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Create a Template' })).not.toBeInTheDocument();
  });

  it('shows step progress text', () => {
    renderWizard({ hasClusters: false });
    expect(screen.getByText('Step 1 of 3')).toBeInTheDocument();
  });

  it('shows step 2 of 3 when on template step', () => {
    renderWizard({ hasClusters: true, hasTemplates: false });
    expect(screen.getByText('Step 2 of 3')).toBeInTheDocument();
  });
});
