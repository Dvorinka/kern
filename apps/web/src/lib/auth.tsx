import { createContext, useContext, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "./api";
import type { User } from "./types";

interface AuthState {
	user: User | null;
	prefs: Record<string, unknown> | null;
	loading: boolean;
	login: (email: string, password: string) => Promise<void>;
	logout: () => Promise<void>;
}

const AuthCtx = createContext<AuthState>({
	user: null,
	prefs: null,
	loading: true,
	login: async () => {},
	logout: async () => {},
});

export function AuthProvider({ children }: { children: ReactNode }) {
	const qc = useQueryClient();
	const { data, isLoading } = useQuery({
		queryKey: ["me"],
		queryFn: async () => {
			try {
				const res = await api.get<{ user: User; prefs?: Record<string, unknown> }>("/auth/me");
				return { user: res.user, prefs: res.prefs ?? {} };
			} catch {
				return null;
			}
		},
		retry: false,
		staleTime: 60_000,
	});

	const state: AuthState = {
		user: data?.user ?? null,
		prefs: data?.prefs ?? null,
		loading: isLoading,
		login: async (email, password) => {
			await api.post("/auth/login", { email, password });
			await qc.invalidateQueries({ queryKey: ["me"] });
		},
		logout: async () => {
			await api.post("/auth/logout");
			qc.clear();
		},
	};
	return <AuthCtx.Provider value={state}>{children}</AuthCtx.Provider>;
}

export const useAuth = () => useContext(AuthCtx);
