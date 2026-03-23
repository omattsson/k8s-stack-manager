import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import EntityLink from '../index';

describe('EntityLink', () => {
  it('renders a link to a template', () => {
    render(
      <MemoryRouter>
        <EntityLink entityType="stack_template" entityId="tpl-1" />
      </MemoryRouter>,
    );
    const link = screen.getByRole('link', { name: 'tpl-1' });
    expect(link).toBeInTheDocument();
    expect(link).toHaveAttribute('href', '/templates/tpl-1');
  });

  it('renders a link to a stack definition', () => {
    render(
      <MemoryRouter>
        <EntityLink entityType="stack_definition" entityId="def-1" />
      </MemoryRouter>,
    );
    const link = screen.getByRole('link', { name: 'def-1' });
    expect(link).toBeInTheDocument();
    expect(link).toHaveAttribute('href', '/stack-definitions/def-1/edit');
  });

  it('renders a link to a stack instance', () => {
    render(
      <MemoryRouter>
        <EntityLink entityType="stack_instance" entityId="inst-1" />
      </MemoryRouter>,
    );
    const link = screen.getByRole('link', { name: 'inst-1' });
    expect(link).toBeInTheDocument();
    expect(link).toHaveAttribute('href', '/stack-instances/inst-1');
  });

  it('renders plain text for an unknown entity type', () => {
    render(
      <MemoryRouter>
        <EntityLink entityType="unknown_type" entityId="xyz-1" />
      </MemoryRouter>,
    );
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
    expect(screen.getByText('xyz-1')).toBeInTheDocument();
  });

  it('displays the entity ID as text', () => {
    render(
      <MemoryRouter>
        <EntityLink entityType="stack_instance" entityId="my-id-123" />
      </MemoryRouter>,
    );
    expect(screen.getByText('my-id-123')).toBeInTheDocument();
  });
});
