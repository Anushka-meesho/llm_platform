import { useState, useEffect } from 'react';
import { Typography, Button, cn } from '@meesho/merlin-ui-tailwind';
import { useAuth } from '../auth/useAuth';
import { api } from '../api/client';
import { setPricing } from '../utils/tokens';
import ComparePage from '../pages/ComparePage';
import TasksPage from '../pages/TasksPage';
import EstimatePage from '../pages/EstimatePage';
import DashboardPage from '../pages/DashboardPage';

type TView = 'compare' | 'tasks' | 'estimate' | 'dashboard';

const NAV: { key: TView; label: string; icon: string }[] = [
  { key: 'compare', label: 'Compare', icon: '💬' },
  { key: 'tasks', label: 'Tasks', icon: '🗂️' },
  { key: 'estimate', label: 'Estimate', icon: '🧮' },
  { key: 'dashboard', label: 'Dashboard', icon: '📊' },
];

const AppShell = () => {
  const { user, logout } = useAuth();
  const [view, setView] = useState<TView>('compare');

  // Sync the frontend's pricing table with the backend's once we're in.
  useEffect(() => {
    api
      .pricing()
      .then((d) => setPricing(d.pricing))
      .catch(() => {
        /* keep fallback rates */
      });
  }, []);

  return (
    <div className="flex flex-col h-screen overflow-hidden bg-primary-bg">
      {/* Top nav */}
      <header className="flex items-center gap-6 px-4 h-14 flex-shrink-0 bg-secondary-bg border-b border-solid border-primary-border">
        <Typography variant="heading" size="5" className="text-primary-text whitespace-nowrap">
          ⚡ LLM Platform
        </Typography>

        <nav className="flex items-center gap-1">
          {NAV.map((item) => (
            <button
              key={item.key}
              onClick={() => setView(item.key)}
              className={cn(
                'px-3 py-1.5 rounded-md text-sm transition-colors',
                view === item.key
                  ? 'bg-tertiary-bg text-primary-text font-medium'
                  : 'text-secondary-text hover:bg-tertiary-bg',
              )}
            >
              <span className="mr-1.5">{item.icon}</span>
              {item.label}
            </button>
          ))}
        </nav>

        <div className="ml-auto flex items-center gap-3">
          {user && (
            <div className="flex items-center gap-2">
              <span className="flex h-7 w-7 items-center justify-center rounded-full bg-tertiary-bg text-xs font-semi-bold text-primary-text">
                {user.name.charAt(0)}
              </span>
              <div className="flex flex-col leading-tight">
                <Typography variant="body" size="2" className="text-primary-text font-medium">
                  {user.name}
                </Typography>
                <span className="text-[10px] text-tertiary-text">
                  {user.email}
                  {user.role && (
                    <span className="ml-1 rounded bg-tertiary-bg px-1 py-px uppercase tracking-wide text-primary-text">
                      {user.role}
                    </span>
                  )}
                </span>
              </div>
            </div>
          )}
          <Button variant="ghost" size="s" onClick={logout}>
            Logout
          </Button>
        </div>
      </header>

      {/* Active view */}
      {view === 'compare' && <ComparePage />}
      {view === 'tasks' && <TasksPage />}
      {view === 'estimate' && <EstimatePage />}
      {view === 'dashboard' && <DashboardPage />}
    </div>
  );
};

export default AppShell;
