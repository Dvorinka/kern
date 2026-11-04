import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import "./index.css";
import { AuthProvider } from "./lib/auth";
import { Layout } from "./components/layout";
import { LoginPage } from "./routes/login";
import { OverviewPage } from "./routes/overview";
import { ProjectsPage, ProjectDetailPage } from "./routes/projects";
import { RunsPage } from "./routes/runs";
import { RunDetailPage } from "./routes/run-detail";
import { RunnersPage } from "./routes/runners";
import { SecretsPage } from "./routes/secrets";
import { ArtifactsPage } from "./routes/artifacts";
import { CachePage } from "./routes/cache";
import { DeliveriesPage } from "./routes/deliveries";
import { SettingsPage } from "./routes/settings";

const router = createBrowserRouter([
	{ path: "/login", element: <LoginPage /> },
	{
		path: "/",
		element: <Layout />,
		children: [
			{ index: true, element: <OverviewPage /> },
			{ path: "projects", element: <ProjectsPage /> },
			{ path: "projects/:slug", element: <ProjectDetailPage /> },
			{ path: "runs", element: <RunsPage /> },
			{ path: "runs/:id", element: <RunDetailPage /> },
			{ path: "runners", element: <RunnersPage /> },
			{ path: "secrets", element: <SecretsPage /> },
			{ path: "artifacts", element: <ArtifactsPage /> },
			{ path: "cache", element: <CachePage /> },
			{ path: "deliveries", element: <DeliveriesPage /> },
			{ path: "settings", element: <SettingsPage /> },
		],
	},
]);

const qc = new QueryClient({
	defaultOptions: { queries: { retry: 1, refetchOnWindowFocus: false } },
});

createRoot(document.getElementById("root")!).render(
	<StrictMode>
		<QueryClientProvider client={qc}>
			<AuthProvider>
				<RouterProvider router={router} />
			</AuthProvider>
		</QueryClientProvider>
	</StrictMode>,
);
