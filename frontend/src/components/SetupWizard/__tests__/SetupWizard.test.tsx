import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import SetupWizard from '../index';

const renderWizard = (props: Partial<React.ComponentProps<typeof SetupWizard>> = {}) =>
  render(
    <MemoryRouter>
      <SetupWizard
        hasClusters={false}
        hasTemplates={false}
        hasInstances={false}
        isAdmin={true}
        onDismiss={vi.fn()}
        {...props}
      />
    </MemoryRouter>,
  );

describe('SetupWizard', () => {
  it('renders welcome heading', () => {
    renderWizard();
    expect(screen.getByText('Welcome to Stack Manager')).toBeInTheDocument();
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
    const onDismiss = vi.fn();
    renderWizard({ onDismiss });
    await userEvent.click(screen.getByText('Skip setup'));
    expect(onDismiss).toHaveBeenCalledOnce();
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
