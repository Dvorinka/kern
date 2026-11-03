// Thin fetch client. Cookie auth rides along same-origin; API tokens use Bearer.

export class ApiError extends Error {
	status: number;
	code: string;
	constructor(status: number, code: string, message: string) {
		super(message);
		this.status = status;
		this.code = code;
	}
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
	const res = await fetch(path, {
		credentials: "same-origin",
		headers: init?.body ? { "Content-Type": "application/json" } : undefined,
		...init,
	});
	if (res.status === 204) return undefined as T;
	const text = await res.text();
	const data = text ? JSON.parse(text) : undefined;
	if (!res.ok) {
		const err = data?.error;
		throw new ApiError(res.status, err?.code ?? "error", err?.message ?? res.statusText);
	}
	return data as T;
}

const base = "/api/v1";

export const api = {
	get: <T>(path: string) => request<T>(`${base}${path}`),
	post: <T>(path: string, body?: unknown) =>
		request<T>(`${base}${path}`, { method: "POST", body: body ? JSON.stringify(body) : undefined }),
	put: <T>(path: string, body?: unknown) =>
		request<T>(`${base}${path}`, { method: "PUT", body: body ? JSON.stringify(body) : undefined }),
	del: <T>(path: string) => request<T>(`${base}${path}`, { method: "DELETE" }),
};
