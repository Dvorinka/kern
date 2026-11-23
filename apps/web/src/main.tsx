import { StrictMode, Suspense, lazy } from "react";
import { createRoot } from "react-dom/client";
import { createBrowserRouter, RouterProvider } from "react-router-dom";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import "./index.css";
import { AuthProvider } from "./lib/auth";
import { Layout } from "./components/layout";
import { Spinner } from "./components/ui";

// Route-level code splitting keeps the initial JS bundle lean and fast.
const LoginPage = lazy(() => import("./routes/login").then((m) => ({ default: m.LoginPage })));
const OverviewPage = lazy(() =>
	import("./routes/overview").then((m) => ({ default: m.OverviewPage })),
);
const ProjectsPage = lazy(() =>
	import("./routes/projects").then((m) => ({ default: m.ProjectsPage })),
);
const ProjectDetailPage = lazy(() =>
	import("./routes/projects").then((m) => ({ default: m.ProjectDetailPage })),
);
const RunsPage = lazy(() => import("./routes/runs").then((m) => ({ default: m.RunsPage })));
const RunDetailPage = lazy(() =>
	import("./routes/run-detail").then((m) => ({ default: m.RunDetailPage })),
);
const RunnersPage = lazy(() =>
	import("./routes/runners").then((m) => ({ default: m.RunnersPage })),
);
const SecretsPage = lazy(() =>
	import("./routes/secrets").then((m) => ({ default: m.SecretsPage })),
);
const ArtifactsPage = lazy(() =>
	import("./routes/artifacts").then((m) => ({ default: m.ArtifactsPage })),
);
const CachePage = lazy(() => import("./routes/cache").then((m) => ({ default: m.CachePage })));
const DeliveriesPage = lazy(() =>
	import("./routes/deliveries").then((m) => ({ default: m.DeliveriesPage })),
);
const SettingsPage = lazy(() =>
	import("./routes/settings").then((m) => ({ default: m.SettingsPage })),
);
const UsagePage = lazy(() => import("./routes/usage").then((m) => ({ default: m.UsagePage })));
const EnvironmentsPage = lazy(() =>
	import("./routes/environments").then((m) => ({ default: m.EnvironmentsPage })),
);
const PublicRunPage = lazy(() =>
	import("./routes/public-run").then((m) => ({ default: m.PublicRunPage })),
);

const load = (el: React.ReactNode) => <Suspense fallback={<Spinner />}>{el}</Suspense>;

const router = createBrowserRouter([
	{ path: "/login", element: load(<LoginPage />) },
	{ path: "/ci/:id", element: load(<PublicRunPage />) },
	{
		path: "/",
		element: <Layout />,
		children: [
			{ index: true, element: load(<OverviewPage />) },
			{ path: "projects", element: load(<ProjectsPage />) },
			{ path: "projects/:slug", element: load(<ProjectDetailPage />) },
			{ path: "runs", element: load(<RunsPage />) },
			{ path: "runs/:id", element: load(<RunDetailPage />) },
			{ path: "runners", element: load(<RunnersPage />) },
			{ path: "secrets", element: load(<SecretsPage />) },
			{ path: "environments", element: load(<EnvironmentsPage />) },
			{ path: "artifacts", element: load(<ArtifactsPage />) },
			{ path: "cache", element: load(<CachePage />) },
			{ path: "deliveries", element: load(<DeliveriesPage />) },
			{ path: "usage", element: load(<UsagePage />) },
			{ path: "settings", element: load(<SettingsPage />) },
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
