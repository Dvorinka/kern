import { api } from "./api";

// GitHub's app-manifest flow must leave the browser as a form POST — fetch
// the manifest, then submit it to github.com so the app is created under
// the user's own account. GitHub redirects back to the KERN callback, which
// lands the user on the install flow.
export async function connectGitHubApp(): Promise<void> {
	const data = await api.get<{ url: string; manifest: Record<string, unknown> }>(
		"/github-app/manifest",
	);
	const form = document.createElement("form");
	form.method = "post";
	form.action = data.url;
	const input = document.createElement("input");
	input.type = "hidden";
	input.name = "manifest";
	input.value = JSON.stringify(data.manifest);
	form.appendChild(input);
	document.body.appendChild(form);
	form.submit();
}
