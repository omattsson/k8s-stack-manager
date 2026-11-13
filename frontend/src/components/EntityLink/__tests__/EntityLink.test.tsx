import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import EntityLink, { entityPath } from '../index';

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

  it('renders the link as an MUI Link', () => {
    render(
      <MemoryRouter>
        <EntityLink entityType="stack_instance" entityId="inst-2" />
      </MemoryRouter>,
    );
    const link = screen.getByRole('link', { name: 'inst-2' });
    expect(link).toHaveClass('MuiLink-root');
    expect(link).toHaveAttribute('href', '/stack-instances/inst-2');
    expect(link).toHaveTextContent('inst-2');
  });

  it('does not render an MUI Link for an unknown entity type', () => {
    const { container } = render(
      <MemoryRouter>
        <EntityLink entityType="unknown_type" entityId="xyz-2" />
      </MemoryRouter>,
    );
    expect(container.querySelector('.MuiLink-root')).toBeNull();
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

describe('entityPath', () => {
  it.each([
    ['stack_instance', 'i1', 'create', false, '/stack-instances/i1'],
    ['stack_template', 't1', 'update', false, '/templates/t1'],
    ['stack_definition', 'd1', 'update', false, '/stack-definitions/d1/edit'],
    ['cluster', 'c1', 'update', true, '/admin/clusters'],
    ['user', 'u1', 'change_role', true, '/admin/users'],
    ['cluster', 'c1', 'update', false, null],
    ['user', 'u1', 'login', false, null],
    ['stack_instance', 'i1', 'delete', false, null],
    ['stack_instance', '', 'create', false, null],
    ['favorite', 'f1', 'create', true, null],
  ])('%s %s %s admin=%s gives %s', (type, id, action, isAdmin, want) => {
    expect(entityPath(type, id, action, isAdmin)).toBe(want);
  });
});

describe('EntityLink with name and action', () => {
  it('shows the name as link text', () => {
    render(
      <MemoryRouter>
        <EntityLink entityType="stack_instance" entityId="i1" entityName="stack-a" action="create" />
      </MemoryRouter>,
    );
    expect(screen.getByRole('link', { name: 'stack-a' })).toHaveAttribute('href', '/stack-instances/i1');
  });

  it('shows no link for a delete', () => {
    render(
      <MemoryRouter>
        <EntityLink entityType="stack_instance" entityId="i1" entityName="stack-a" action="delete" />
      </MemoryRouter>,
    );
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
    expect(screen.getByText('stack-a')).toBeInTheDocument();
  });

  it('shows no admin link for a non-admin', () => {
    render(
      <MemoryRouter>
        <EntityLink entityType="cluster" entityId="c1" action="update" />
      </MemoryRouter>,
    );
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
  });
});
