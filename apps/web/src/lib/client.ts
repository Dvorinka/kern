// Typed client generated from openapi.yaml (src/lib/api.gen.ts). New API
// domains should use this; routes not yet covered stay on api.get/post in
// api.ts until their domain migrates.
import createClient from "openapi-fetch";
import type { paths } from "./api.gen";
import { ApiError } from "./api";

export const client = createClient<paths>({
	baseUrl: "/api/v1",
	credentials: "same-origin",
});

type FetchResult<T> = { data?: T; error?: unknown; response: Response };

// unwrap converts openapi-fetch's {data,error,response} result into the
// throw-on-error style api.ts uses, so query/mutation call sites stay
// idiomatic. The spec's Error schema types `error` already.
export function unwrap<T>(r: FetchResult<T>): T {
	if (r.error !== undefined || !r.response.ok) {
		const e = (
			r.error as { error?: { code?: string; message?: string } } | undefined
		)?.error;
		throw new ApiError(
			r.response.status,
			e?.code ?? "error",
			e?.message ?? r.response.statusText,
		);
	}
	return r.data as T;
}
