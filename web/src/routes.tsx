import { createBrowserRouter } from "react-router-dom";
import { Layout } from "./components/Layout";
import { OverviewPage } from "./pages/OverviewPage";
import { DiscoveryPage } from "./pages/DiscoveryPage";
import { CatalogPage } from "./pages/CatalogPage";

// The server's SPA fallback rewrites unknown paths to index.html, so these
// client routes need no server-side counterpart.
export const router = createBrowserRouter([
  {
    path: "/",
    element: <Layout />,
    children: [
      { index: true, element: <OverviewPage /> },
      { path: "discovery", element: <DiscoveryPage /> },
      { path: "catalog", element: <CatalogPage /> },
    ],
  },
]);
