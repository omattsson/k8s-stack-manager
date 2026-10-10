import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import Layout from '../index';
import { StaticBrandingProvider } from '../../../context/BrandingContext';

vi.mock('../../../context/AuthContext', () => ({
  useAuth: vi.fn(),
}));

vi.mock('../../NotificationCenter', () => ({
  default: () => <div data-testid="notification-center">NotificationCenter</div>,
}));

vi.mock('../../../context/ThemeContext', () => ({
  useThemeMode: () => ({
    mode: 'light' as const,
    toggleMode: vi.fn(),
  }),
}));

import { useAuth } from '../../../context/AuthContext';

const authenticatedUser = {
  id: '1',
  username: 'testuser',
  role: 'admin',
  display_name: 'Test User',
  created_at: '',
  updated_at: '',
};

describe('Layout', () => {
  afterEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
  });

  it('renders children in the main content area when not authenticated', () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: null,
      isAuthenticated: false,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });

    render(
      <MemoryRouter>
        <Layout>
          <div>Page content</div>
        </Layout>
      </MemoryRouter>,
    );

    expect(screen.getByText('Page content')).toBeInTheDocument();
  });

  it('does not render navigation when not authenticated', () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: null,
      isAuthenticated: false,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });

    render(
      <MemoryRouter>
        <Layout>
          <div>Content</div>
        </Layout>
      </MemoryRouter>,
    );

    expect(screen.queryByRole('navigation', { name: 'Main navigation' })).not.toBeInTheDocument();
  });

  it('renders navigation when authenticated', () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: authenticatedUser,
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });

    render(
      <MemoryRouter>
        <Layout>
          <div>Content</div>
        </Layout>
      </MemoryRouter>,
    );

    expect(screen.getByRole('navigation', { name: 'Main navigation' })).toBeInTheDocument();
  });

  it('renders main nav links when authenticated', () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: authenticatedUser,
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });

    render(
      <MemoryRouter>
        <Layout>
          <div>Content</div>
        </Layout>
      </MemoryRouter>,
    );

    expect(screen.getByText('Dashboard')).toBeInTheDocument();
    expect(screen.getByText('Templates')).toBeInTheDocument();
    expect(screen.getByText('Definitions')).toBeInTheDocument();
    expect(screen.getByText('Audit Log')).toBeInTheDocument();
  });

  it('renders admin nav links for admin users', () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: authenticatedUser,
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });

    render(
      <MemoryRouter>
        <Layout>
          <div>Content</div>
        </Layout>
      </MemoryRouter>,
    );

    expect(screen.getByText('Users')).toBeInTheDocument();
    expect(screen.getByText('Clusters')).toBeInTheDocument();
  });

  it('does not render admin nav links for regular users', () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: { ...authenticatedUser, role: 'user' },
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });

    render(
      <MemoryRouter>
        <Layout>
          <div>Content</div>
        </Layout>
      </MemoryRouter>,
    );

    expect(screen.queryByText('Users')).not.toBeInTheDocument();
    expect(screen.queryByText('Clusters')).not.toBeInTheDocument();
  });

  it('shows operations nav for devops users', () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: { ...authenticatedUser, role: 'devops' },
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });

    render(
      <MemoryRouter>
        <Layout>
          <div>Content</div>
        </Layout>
      </MemoryRouter>,
    );

    expect(screen.getByText('Cluster Health')).toBeInTheDocument();
    expect(screen.getByText('Analytics')).toBeInTheDocument();
  });

  it.each([
    ['user', false],
    ['devops', true],
    ['admin', true],
  ])('shows the Audit Log nav item for role %s: %s', (role, visible) => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: { ...authenticatedUser, role },
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });

    render(
      <MemoryRouter>
        <Layout>
          <div>Content</div>
        </Layout>
      </MemoryRouter>,
    );

    // The Dashboard item has no role limit and is always present.
    expect(screen.getByText('Dashboard')).toBeInTheDocument();
    if (visible) {
      expect(screen.getByText('Audit Log')).toBeInTheDocument();
    } else {
      expect(screen.queryByText('Audit Log')).not.toBeInTheDocument();
    }
  });

  it('renders the username when authenticated', () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: authenticatedUser,
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });

    render(
      <MemoryRouter>
        <Layout>
          <div>Content</div>
        </Layout>
      </MemoryRouter>,
    );

    expect(screen.getByText('testuser')).toBeInTheDocument();
  });

  it('renders children in the main content area when authenticated', () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: authenticatedUser,
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });

    render(
      <MemoryRouter>
        <Layout>
          <div>Authenticated page content</div>
        </Layout>
      </MemoryRouter>,
    );

    expect(screen.getByText('Authenticated page content')).toBeInTheDocument();
  });

  it('renders the app title link', () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: authenticatedUser,
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });

    render(
      <MemoryRouter>
        <Layout>
          <div>Content</div>
        </Layout>
      </MemoryRouter>,
    );

    expect(screen.getByText('K8s Stack Manager')).toBeInTheDocument();
  });

  it('shows the configured title and logo in the sidebar', () => {
    (useAuth as ReturnType<typeof vi.fn>).mockReturnValue({
      user: authenticatedUser,
      isAuthenticated: true,
      isLoading: false,
      login: vi.fn(),
      logout: vi.fn(),
    });

    render(
      <StaticBrandingProvider value={{ title: 'Platform Portal', logoUrl: '/branding/logo.svg', faviconUrl: '/f.svg' }}>
        <MemoryRouter>
          <Layout>
            <div>Content</div>
          </Layout>
        </MemoryRouter>
      </StaticBrandingProvider>,
    );

    expect(screen.getByText('Platform Portal')).toBeInTheDocument();
    expect(screen.queryByText('K8s Stack Manager')).not.toBeInTheDocument();
    const logos = screen.getAllByTestId('brand-logo');
    expect(logos.length).toBeGreaterThan(0);
    logos.forEach((logo) => expect(logo).toHaveAttribute('src', '/branding/logo.svg'));
  });
});
