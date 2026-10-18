import { BrowserRouter } from 'react-router-dom';
import AppRoutes from './routes';
import Layout from './components/Layout';
import ErrorBoundary from './components/ErrorBoundary';
import { AuthProvider } from './context/AuthContext';
import { NotificationProvider } from './context/NotificationContext';
import { ThemeModeProvider } from './context/ThemeContext';
import { BrandingProvider } from './context/BrandingContext';

function App() {
  return (
    <ThemeModeProvider>
      <BrandingProvider>
        <BrowserRouter>
          <NotificationProvider>
            <AuthProvider>
              <Layout>
                <ErrorBoundary>
                  <AppRoutes />
                </ErrorBoundary>
              </Layout>
            </AuthProvider>
          </NotificationProvider>
        </BrowserRouter>
      </BrandingProvider>
    </ThemeModeProvider>
  );
}

export default App;
