import { useState, useEffect, useCallback, type ReactNode } from 'react';
import { api, ApiError } from '../api/client';
import { AuthContext } from './context';
import type { TUser } from '../types';

export const AuthProvider = ({ children }: { children: ReactNode }) => {
  const [user, setUser] = useState<TUser | null>(null);
  const [loading, setLoading] = useState(true);

  // Bootstrap: ask the backend who we are. A 401 simply means "not logged in".
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const { user } = await api.me();
        if (!cancelled) setUser(user);
      } catch (err) {
        if (!cancelled && !(err instanceof ApiError && err.status === 401)) {
          // Network/other errors: stay logged out, but don't crash.
          console.warn('auth bootstrap failed:', err);
        }
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const login = useCallback(async (userId: string) => {
    const { user } = await api.login(userId);
    setUser(user);
  }, []);

  const logout = useCallback(async () => {
    await api.logout().catch(() => {});
    setUser(null);
  }, []);

  return (
    <AuthContext.Provider value={{ user, loading, login, logout }}>
      {children}
    </AuthContext.Provider>
  );
};
