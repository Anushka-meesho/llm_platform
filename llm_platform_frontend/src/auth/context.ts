import { createContext } from 'react';
import type { TUser } from '../types';

export type TAuthContext = {
  user: TUser | null;
  loading: boolean;
  login: (userId: string) => Promise<void>;
  logout: () => Promise<void>;
};

export const AuthContext = createContext<TAuthContext | null>(null);
