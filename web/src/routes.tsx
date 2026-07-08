import { createBrowserRouter } from "react-router-dom";
import { Layout } from "./components/Layout";
import { RunsPage } from "./pages/RunsPage";
import { DefinitionsPage } from "./pages/DefinitionsPage";
import { CatalogPage } from "./pages/CatalogPage";
import { ListingsPage } from "./pages/ListingsPage";

// The server's SPA fallback rewrites unknown paths to index.html, so these
// client routes need no server-side counterpart.
export const router = createBrowserRouter([
  {
    path: "/",
    element: <Layout />,
    children: [
      { index: true, element: <RunsPage /> },
      { path: "definitions", element: <DefinitionsPage /> },
      { path: "catalog", element: <CatalogPage /> },
      { path: "listings", element: <ListingsPage /> },
    ],
  },
]);
