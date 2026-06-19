import { useState, useEffect, type ReactNode } from 'react';
import { Typography, Button, cn } from '@meesho/merlin-ui-tailwind';
import { usePersistentState } from '../hooks/usePersistentState';
import { useAuth } from '../auth/useAuth';
import { api, errorMessage } from '../api/client';
import { setPricing } from '../utils/tokens';
import ComparePage from '../pages/ComparePage';
import TasksPage from '../pages/TasksPage';
import DashboardPage from '../pages/DashboardPage';
import AdminRunsPage from '../pages/AdminRunsPage';
import ModelHealthPage from '../pages/ModelHealthPage';
import ClientPortalPage from '../pages/ClientPortalPage';

type TView =
  | 'compare'
  | 'tasks'
  | 'dashboard'
  | 'history'
  | 'health'
  | 'test';

const NAV: { key: TView; label: string; icon: string; adminOnly?: boolean }[] = [
  { key: 'compare', label: 'Compare', icon: '💬' },
  { key: 'tasks', label: 'Tasks', icon: '🗂️' },
  { key: 'dashboard', label: 'Dashboard', icon: '📊' },
  { key: 'history', label: 'History', icon: '🗂', adminOnly: true },
  { key: 'health', label: 'Health', icon: '🫀', adminOnly: true },
  { key: 'test', label: 'Test', icon: '🧪', adminOnly: true },
];

const AppShell = () => {
  const { user, logout } = useAuth();
  // The active tab persists across reloads, so you return to the page you left.
  const [view, setView] = usePersistentState<TView>('ui.activeTab', 'compare');

  const isAdmin = user?.role === 'admin';
  const nav = NAV.filter((item) => !item.adminOnly || isAdmin);
  // A persisted tab the current user can't see (e.g. an admin tab after role
  // change) falls back to Compare so the page never renders blank.
  const activeView: TView = nav.some((n) => n.key === view) ? view : 'compare';

  // Tabs are kept mounted once visited (and just hidden when inactive), so each
  // keeps its own state — scroll, filters, in-progress chat/edits — when you
  // switch away and come back. We track which tabs have been opened so each is
  // mounted lazily on first visit rather than firing every page's data fetches
  // at login. visited is updated in the nav handler (not an effect) to avoid a
  // cascading render; it's seeded with the restored tab so a reload mounts it.
  const [visited, setVisited] = useState<Set<TView>>(
    () => new Set<TView>(['compare', activeView]),
  );
  const goTo = (key: TView) => {
    setView(key);
    setVisited((prev) => (prev.has(key) ? prev : new Set(prev).add(key)));
  };

  // Sync the frontend's pricing table with the backend's once we're in.
  useEffect(() => {
    api
      .pricing()
      .then((d) => setPricing(d.pricing))
      .catch((e) => {
        // Non-critical: keep fallback rates, just log.
        console.error('pricing sync failed:', errorMessage(e));
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
          {nav.map((item) => (
            <button
              key={item.key}
              onClick={() => goTo(item.key)}
              className={cn(
                'px-3 py-1.5 rounded-md text-sm transition-colors',
                activeView === item.key
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

      {/* Views: each visited tab stays mounted and is hidden (not unmounted)
          when inactive, so its state survives tab switches. */}
      {(
        [
          { key: 'compare', node: <ComparePage />, adminOnly: false },
          { key: 'tasks', node: <TasksPage />, adminOnly: false },
          { key: 'dashboard', node: <DashboardPage />, adminOnly: false },
          { key: 'history', node: <AdminRunsPage />, adminOnly: true },
          { key: 'health', node: <ModelHealthPage />, adminOnly: true },
          { key: 'test', node: <ClientPortalPage />, adminOnly: true },
        ] as { key: TView; node: ReactNode; adminOnly: boolean }[]
      ).map(({ key, node, adminOnly }) => {
        if (adminOnly && !isAdmin) return null;
        if (!visited.has(key)) return null; // not opened yet — mount on first visit
        return (
          <div key={key} className={cn('flex-1 min-h-0 flex flex-col', activeView !== key && 'hidden')}>
            {node}
          </div>
        );
      })}
    </div>
  );
};

export default AppShell;
